package nodeagent

import (
	"archive/tar"
	"compress/gzip"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
)

const (
	// maxPackageSize bounds both the downloaded archive and the
	// unpacked content; lab packages are small static binaries.
	maxPackageSize = 256 << 20

	// keepVersions is how many version directories of one package the
	// store retains beyond those referenced by installed programs.
	keepVersions = 3

	// shaMarker inside a version directory records the archive digest
	// the directory was unpacked from; its presence marks a complete
	// installation.
	shaMarker = ".dcnetlab-sha256"
)

var versionRE = regexp.MustCompile(`^[a-zA-Z0-9][a-zA-Z0-9.+_-]{0,63}$`)

// PackageRef identifies one package version and where to fetch it;
// it mirrors the wire message.
type PackageRef struct {
	Name       string
	Version    string
	SHA256     string
	URL        string
	Entrypoint string
	// Links are absolute paths symlinked to the installed entrypoint,
	// so packaged tools appear at their conventional locations.
	Links []string
}

// InstallPackage makes a package version available in the local
// store: download, digest check, unpack, atomically rename into
// place. A version already present with the same digest is a no-op;
// a digest mismatch (replaced builtin) is re-downloaded.
func (m *Manager) InstallPackage(ctx context.Context, ref PackageRef) error {
	if !nameRE.MatchString(ref.Name) {
		return fmt.Errorf("invalid package name %q", ref.Name)
	}

	if !versionRE.MatchString(ref.Version) {
		return fmt.Errorf("invalid package version %q", ref.Version)
	}

	if ref.Entrypoint == "" || !filepath.IsLocal(ref.Entrypoint) {
		return fmt.Errorf("invalid entrypoint %q", ref.Entrypoint)
	}

	m.pkgMu.Lock()
	defer m.pkgMu.Unlock()

	dir := m.packageDir(ref.Name, ref.Version)
	if digest, err := os.ReadFile(filepath.Join(dir, shaMarker)); err == nil && string(digest) == ref.SHA256 {
		// Already installed; still re-assert the links, they may have
		// been requested only on this install.
		return ensureLinks(filepath.Join(dir, ref.Entrypoint), ref.Links)
	}

	archive, err := m.download(ctx, ref)
	if err != nil {
		return err
	}

	defer func() { _ = os.Remove(archive) }()

	tmp := dir + ".tmp"
	_ = os.RemoveAll(tmp)
	if err := unpack(archive, tmp); err != nil {
		_ = os.RemoveAll(tmp)

		return fmt.Errorf("unpack %s@%s: %w", ref.Name, ref.Version, err)
	}

	entry := filepath.Join(tmp, ref.Entrypoint)
	if st, err := os.Stat(entry); err != nil || !st.Mode().IsRegular() {
		_ = os.RemoveAll(tmp)

		return fmt.Errorf("package %s@%s has no entrypoint %s", ref.Name, ref.Version, ref.Entrypoint)
	}

	if err := os.Chmod(entry, 0o755); err != nil {
		_ = os.RemoveAll(tmp)

		return fmt.Errorf("mark entrypoint executable: %w", err)
	}

	if err := os.WriteFile(filepath.Join(tmp, shaMarker), []byte(ref.SHA256), 0o644); err != nil {
		_ = os.RemoveAll(tmp)

		return fmt.Errorf("write digest marker: %w", err)
	}

	_ = os.RemoveAll(dir)
	if err := os.Rename(tmp, dir); err != nil {
		_ = os.RemoveAll(tmp)

		return fmt.Errorf("activate package dir: %w", err)
	}

	if err := ensureLinks(filepath.Join(dir, ref.Entrypoint), ref.Links); err != nil {
		return err
	}

	m.log.Info("package installed", "name", ref.Name, "version", ref.Version)
	m.rememberRepo(ref.URL)
	m.gcPackageLocked(ref.Name)

	return nil
}

// ensureLinks symlinks the installed entrypoint at the requested
// absolute paths (ln -sf semantics: an existing target is replaced).
func ensureLinks(entry string, links []string) error {
	for _, link := range links {
		if !filepath.IsAbs(link) {
			return fmt.Errorf("link path %q is not absolute", link)
		}

		if err := os.MkdirAll(filepath.Dir(link), 0o755); err != nil {
			return fmt.Errorf("link %s: %w", link, err)
		}

		_ = os.Remove(link)
		if err := os.Symlink(entry, link); err != nil {
			return fmt.Errorf("link %s: %w", link, err)
		}
	}

	return nil
}

// InUseVersion is one package version kept during removal and the
// programs pinning it.
type InUseVersion struct {
	Version  string
	Programs []string
}

// RemovePackage deletes locally installed versions of a package: the
// named one, or every version when version is empty. Versions
// referenced by installed programs are kept and reported in inUse
// together with the referencing program names.
func (m *Manager) RemovePackage(name, version string) (removed []string, inUse []InUseVersion, err error) {
	if !nameRE.MatchString(name) {
		return nil, nil, fmt.Errorf("invalid package name %q", name)
	}

	if version != "" && !versionRE.MatchString(version) {
		return nil, nil, fmt.Errorf("invalid package version %q", version)
	}

	m.pkgMu.Lock()
	defer m.pkgMu.Unlock()

	root := filepath.Join(m.dir, "packages", name)
	var versions []string
	if version != "" {
		if _, statErr := os.Stat(filepath.Join(root, version)); statErr != nil {
			return nil, nil, fmt.Errorf("package %s@%s not installed", name, version)
		}

		versions = []string{version}
	} else {
		entries, readErr := os.ReadDir(root)
		if readErr != nil || len(entries) == 0 {
			return nil, nil, fmt.Errorf("package %s not installed", name)
		}

		for _, e := range entries {
			if e.IsDir() {
				versions = append(versions, e.Name())
			}
		}
	}

	referenced := make(map[string][]string)
	m.mu.Lock()
	for _, p := range m.programs {
		if p.spec.PackageName == name {
			referenced[p.spec.PackageVersion] = append(referenced[p.spec.PackageVersion], p.spec.Name)
		}
	}

	m.mu.Unlock()

	for _, v := range versions {
		if programs := referenced[v]; len(programs) > 0 {
			sort.Strings(programs)
			inUse = append(inUse, InUseVersion{Version: v, Programs: programs})

			continue
		}

		if err := os.RemoveAll(filepath.Join(root, v)); err != nil {
			return removed, inUse, fmt.Errorf("remove %s@%s: %w", name, v, err)
		}

		removed = append(removed, v)
		m.log.Info("package version removed", "name", name, "version", v)
	}

	// Drop the now empty package directory, best effort.
	if entries, err := os.ReadDir(root); err == nil && len(entries) == 0 {
		_ = os.Remove(root)
	}

	return removed, inUse, nil
}

// InstalledPackage is one complete package version in the local
// store.
type InstalledPackage struct {
	Name    string
	Version string
	SHA256  string
}

// ListPackages walks the local store and returns every complete
// (digest-marked) package version, sorted by name then version.
func (m *Manager) ListPackages() ([]InstalledPackage, error) {
	m.pkgMu.Lock()
	defer m.pkgMu.Unlock()

	root := filepath.Join(m.dir, "packages")
	names, err := os.ReadDir(root)
	if err != nil {
		return nil, fmt.Errorf("read package store: %w", err)
	}

	var packages []InstalledPackage
	for _, n := range names {
		if !n.IsDir() {
			continue
		}

		versions, err := os.ReadDir(filepath.Join(root, n.Name()))
		if err != nil {
			continue
		}

		for _, v := range versions {
			if !v.IsDir() {
				continue
			}

			digest, err := os.ReadFile(filepath.Join(root, n.Name(), v.Name(), shaMarker))
			if err != nil {
				continue // incomplete or foreign directory
			}

			packages = append(packages, InstalledPackage{
				Name: n.Name(), Version: v.Name(), SHA256: strings.TrimSpace(string(digest)),
			})
		}
	}

	sort.Slice(packages, func(i, j int) bool {
		if packages[i].Name != packages[j].Name {
			return packages[i].Name < packages[j].Name
		}

		return packages[i].Version < packages[j].Version
	})

	return packages, nil
}

// repoURLFile remembers the repository base URL of the last install
// so the pkg CLI can find the repository without configuration.
const repoURLFile = "repo.url"

// PackageInstalled reports whether a complete package version sits
// in the store under dir. It is the read-only store query the pkg
// CLI uses; the store layout stays private to this package.
func PackageInstalled(dir, name, version string) bool {
	_, err := os.Stat(filepath.Join(dir, "packages", name, version, shaMarker))

	return err == nil
}

// RememberedRepo returns the repository base the daemon recorded
// under dir, if any.
func RememberedRepo(dir string) (string, bool) {
	data, err := os.ReadFile(filepath.Join(dir, repoURLFile))
	if err != nil {
		return "", false
	}

	base := strings.TrimSpace(string(data))

	return base, base != ""
}

// rememberRepo persists the repository base derived from an archive
// URL, best effort.
func (m *Manager) rememberRepo(rawURL string) {
	u, err := url.Parse(rawURL)
	if err != nil || u.Scheme == "" || u.Host == "" {
		return
	}

	base := u.Scheme + "://" + u.Host
	_ = os.WriteFile(filepath.Join(m.dir, repoURLFile), []byte(base), 0o644)
}

func (m *Manager) packageDir(name, version string) string {
	return filepath.Join(m.dir, "packages", name, version)
}

// download fetches the archive to a temp file, verifying its SHA-256
// on the way, and returns the temp path.
func (m *Manager) download(ctx context.Context, ref PackageRef) (string, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, ref.URL, nil)
	if err != nil {
		return "", fmt.Errorf("build download request: %w", err)
	}

	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return "", fmt.Errorf("download %s: %w", ref.URL, err)
	}

	defer func() { _ = resp.Body.Close() }()

	if resp.StatusCode != http.StatusOK {
		return "", fmt.Errorf("download %s: status %s", ref.URL, resp.Status)
	}

	tmp, err := os.CreateTemp(filepath.Join(m.dir, "packages"), "download-*.tgz")
	if err != nil {
		return "", fmt.Errorf("create download temp: %w", err)
	}

	hash := sha256.New()
	n, err := io.Copy(io.MultiWriter(tmp, hash), io.LimitReader(resp.Body, maxPackageSize+1))
	closeErr := tmp.Close()

	switch {
	case err != nil:
		_ = os.Remove(tmp.Name())

		return "", fmt.Errorf("download %s: %w", ref.URL, err)
	case closeErr != nil:
		_ = os.Remove(tmp.Name())

		return "", fmt.Errorf("write archive: %w", closeErr)
	case n > maxPackageSize:
		_ = os.Remove(tmp.Name())

		return "", fmt.Errorf("package exceeds %d bytes", int64(maxPackageSize))
	}

	if digest := hex.EncodeToString(hash.Sum(nil)); digest != ref.SHA256 {
		_ = os.Remove(tmp.Name())

		return "", fmt.Errorf("digest mismatch: got %s, want %s", digest, ref.SHA256)
	}

	return tmp.Name(), nil
}

// unpack extracts a tar.gz archive into dir, refusing entries that
// escape it (absolute paths, .., links) and bounding the total size.
func unpack(archive, dir string) error {
	f, err := os.Open(archive)
	if err != nil {
		return err
	}

	defer func() { _ = f.Close() }()

	gz, err := gzip.NewReader(f)
	if err != nil {
		return fmt.Errorf("gzip: %w", err)
	}

	tr := tar.NewReader(gz)
	var total int64
	for {
		hdr, err := tr.Next()
		if err == io.EOF {
			return nil
		}

		if err != nil {
			return fmt.Errorf("tar: %w", err)
		}

		name := filepath.Clean(hdr.Name)
		if name == "." {
			continue
		}

		if !filepath.IsLocal(name) {
			return fmt.Errorf("entry %q escapes the package directory", hdr.Name)
		}

		path := filepath.Join(dir, name)
		switch hdr.Typeflag {
		case tar.TypeDir:
			if err := os.MkdirAll(path, 0o755); err != nil {
				return err
			}
		case tar.TypeReg:
			total += hdr.Size
			if total > maxPackageSize {
				return fmt.Errorf("unpacked content exceeds %d bytes", int64(maxPackageSize))
			}

			if err := writeFileFrom(tr, path, hdr.FileInfo().Mode().Perm()); err != nil {
				return err
			}
		default:
			return fmt.Errorf("entry %q has unsupported type %d (only files and directories)", hdr.Name, hdr.Typeflag)
		}
	}
}

// writeFileFrom writes one unpacked regular file.
func writeFileFrom(r io.Reader, path string, perm os.FileMode) error {
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return err
	}

	f, err := os.OpenFile(path, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, perm)
	if err != nil {
		return err
	}

	if _, err := io.Copy(f, r); err != nil {
		_ = f.Close()

		return err
	}

	return f.Close()
}

// gcPackageLocked trims old version directories of one package,
// keeping the newest keepVersions plus anything an installed program
// still references; pkgMu must be held.
func (m *Manager) gcPackageLocked(name string) {
	root := filepath.Join(m.dir, "packages", name)
	entries, err := os.ReadDir(root)
	if err != nil {
		return
	}

	referenced := make(map[string]bool)
	m.mu.Lock()
	for _, p := range m.programs {
		if p.spec.PackageName == name {
			referenced[p.spec.PackageVersion] = true
		}
	}

	m.mu.Unlock()

	type versionDir struct {
		name    string
		modTime int64
	}

	var dirs []versionDir
	for _, e := range entries {
		if !e.IsDir() || referenced[e.Name()] {
			continue
		}

		info, err := e.Info()
		if err != nil {
			continue
		}

		dirs = append(dirs, versionDir{name: e.Name(), modTime: info.ModTime().UnixNano()})
	}

	if len(dirs) <= keepVersions {
		return
	}

	sort.Slice(dirs, func(i, j int) bool { return dirs[i].modTime > dirs[j].modTime })

	for _, d := range dirs[keepVersions:] {
		if err := os.RemoveAll(filepath.Join(root, d.name)); err == nil {
			m.log.Info("package version evicted", "name", name, "version", d.name)
		}
	}
}
