package biz

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/ifantsai/dcnetlab/controller/internal/conf"
	"github.com/ifantsai/dcnetlab/internal/model"
	"github.com/ifantsai/dcnetlab/internal/nodeagentapi"
)

const (
	// maxPackageUpload bounds uploaded archives; lab packages are
	// small static binaries.
	maxPackageUpload = 128 << 20

	// manifestFileName sits at the archive root and declares the
	// package identity.
	manifestFileName = "manifest.json"
)

var (
	packageNameRE    = regexp.MustCompile(`^[a-z0-9][a-z0-9-]{0,62}$`)
	packageVersionRE = regexp.MustCompile(`^[a-zA-Z0-9][a-zA-Z0-9.+_-]{0,63}$`)
)

// PackageManifest is the manifest.json inside a package archive.
type PackageManifest struct {
	Name        string   `json:"name"`
	Version     string   `json:"version"`
	Entrypoint  string   `json:"entrypoint"`
	Description string   `json:"description,omitempty"`
	Links       []string `json:"links,omitempty"`
}

// PackageRepo abstracts package persistence: resource documents in
// the store plus the archive payloads on disk.
type PackageRepo interface {
	CreatePackage(p *model.Package) error
	UpdatePackage(p *model.Package) error
	DeletePackage(name, version string) error
	GetPackage(name, version string) (*model.Package, error)
	ListPackages() ([]*model.Package, error)
	SavePackagePayload(name, version string, payload []byte) error
	PackagePayloadPath(name, version string) (string, error)
	DeletePackagePayload(name, version string) error
}

// PackageUsecase manages the controller's package repository: the
// versioned program artifacts servers install and run. The builtin
// trafficgen is registered as the first package at startup; uploads go
// through the same pipeline.
type PackageUsecase struct {
	repo PackageRepo
	log  *slog.Logger
}

// NewPackageUsecase wires the package usecase and registers the
// bundled trafficgen as the builtin package. A missing trafficgen binary
// only logs (dev and test environments without built artifacts).
func NewPackageUsecase(repo PackageRepo, c *conf.Data, log *slog.Logger) (*PackageUsecase, error) {
	uc := &PackageUsecase{repo: repo, log: log}

	if err := uc.registerBuiltin(c.BinDir); err != nil {
		return nil, fmt.Errorf("register builtin package: %w", err)
	}

	return uc, nil
}

// UploadPackage validates and stores one uploaded tar.gz archive;
// its identity comes from the embedded manifest.json.
func (uc *PackageUsecase) UploadPackage(payload []byte) (*model.Package, error) {
	if len(payload) == 0 {
		return nil, fmt.Errorf("empty package payload")
	}

	if len(payload) > maxPackageUpload {
		return nil, fmt.Errorf("package exceeds %d bytes", maxPackageUpload)
	}

	manifest, err := readArchiveManifest(payload)
	if err != nil {
		return nil, err
	}

	if manifest.Name == model.BuiltinPackageName || manifest.Name == model.CapturePackageName {
		return nil, fmt.Errorf("package name %q is reserved for a builtin package", manifest.Name)
	}

	return uc.storePackage(manifest, payload, false)
}

// GetPackage returns one package version.
func (uc *PackageUsecase) GetPackage(name, version string) (*model.Package, error) {
	return uc.repo.GetPackage(name, version)
}

// PackagePayloadPath exposes where a package archive lives on disk,
// for the repository HTTP endpoint.
func (uc *PackageUsecase) PackagePayloadPath(name, version string) (string, error) {
	if _, err := uc.repo.GetPackage(name, version); err != nil {
		return "", err
	}

	path, err := uc.repo.PackagePayloadPath(name, version)
	if err != nil {
		return "", err
	}

	return path, nil
}

// ListPackages returns every package, sorted by name and descending
// version.
func (uc *PackageUsecase) ListPackages() ([]*model.Package, error) {
	packages, err := uc.repo.ListPackages()
	if err != nil {
		return nil, err
	}

	sort.Slice(packages, func(i, j int) bool {
		if packages[i].Meta.Name != packages[j].Meta.Name {
			return packages[i].Meta.Name < packages[j].Meta.Name
		}

		return compareVersions(packages[i].Spec.Version, packages[j].Spec.Version) > 0
	})

	return packages, nil
}

// DeletePackage removes a package version and its payload; the
// builtin package is protected.
func (uc *PackageUsecase) DeletePackage(name, version string) error {
	p, err := uc.repo.GetPackage(name, version)
	if err != nil {
		return err
	}

	if p.Status.Builtin {
		return fmt.Errorf("package %s@%s is builtin and cannot be deleted", name, version)
	}

	if err := uc.repo.DeletePackagePayload(name, version); err != nil {
		return err
	}

	return uc.repo.DeletePackage(name, version)
}

// registerBuiltin packages the bundled binaries and registers them
// under their builtin identities. A missing binary only logs (dev and
// test environments without built artifacts).
func (uc *PackageUsecase) registerBuiltin(binDir string) error {
	if binDir == "" {
		uc.log.Warn("no bin dir configured; builtin packages not registered")

		return nil
	}

	manifests := []PackageManifest{
		{
			Name:        model.BuiltinPackageName,
			Version:     model.BuiltinPackageVersion,
			Entrypoint:  model.BuiltinPackageEntrypoint,
			Description: "builtin trafficgen: http/tcp/udp server and client modes",
		},
		{
			Name:        model.CapturePackageName,
			Version:     model.CapturePackageVersion,
			Entrypoint:  model.CapturePackageEntrypoint,
			Description: "builtin packet capture tool; switch images bake it in, servers install it from here",
			// The controller invokes the capture path; the PATH link
			// serves operators in a node terminal — mirroring the
			// switch image layout.
			Links: []string{nodeagentapi.CapturePath, "/usr/local/bin/capture"},
		},
	}

	for _, manifest := range manifests {
		if err := uc.registerOneBuiltin(binDir, manifest); err != nil {
			return fmt.Errorf("register builtin %s: %w", manifest.Name, err)
		}
	}

	return nil
}

// registerOneBuiltin stores one bundled binary as a builtin package.
// A changed binary behind the same version (dev builds) replaces the
// stored payload.
func (uc *PackageUsecase) registerOneBuiltin(binDir string, manifest PackageManifest) error {
	binary, err := os.ReadFile(filepath.Join(binDir, manifest.Entrypoint))
	if err != nil {
		uc.log.Warn("builtin binary not found; package not registered", "package", manifest.Name, "error", err)

		return nil
	}

	payload, err := buildArchive(manifest, binary)
	if err != nil {
		return fmt.Errorf("build builtin archive: %w", err)
	}

	digest := payloadDigest(payload)
	existing, err := uc.repo.GetPackage(manifest.Name, manifest.Version)
	switch {
	case errors.Is(err, ErrNotFound):
		_, err := uc.storePackage(manifest, payload, true)

		return err
	case err != nil:
		return err
	case existing.Status.SHA256 == digest:
		return nil
	}

	// Same version, different content: a rebuilt trafficgen. Replace the
	// payload in place so deployed references stay valid.
	if err := uc.repo.SavePackagePayload(manifest.Name, manifest.Version, payload); err != nil {
		return err
	}

	existing.Status.SHA256 = digest
	existing.Status.SizeBytes = int64(len(payload))
	uc.log.Info("builtin package payload replaced", "version", manifest.Version, "sha256", digest[:12])

	return uc.repo.UpdatePackage(existing)
}

// storePackage persists one validated archive as a package resource.
func (uc *PackageUsecase) storePackage(manifest PackageManifest, payload []byte, builtin bool) (*model.Package, error) {
	if _, err := uc.repo.GetPackage(manifest.Name, manifest.Version); err == nil {
		return nil, fmt.Errorf("package %s@%s already exists", manifest.Name, manifest.Version)
	} else if !errors.Is(err, ErrNotFound) {
		return nil, err
	}

	now := time.Now().UTC()
	p := &model.Package{
		Meta: model.ResourceMeta{
			ID: model.NewID("pkg"), Name: manifest.Name, CreatedAt: now, UpdatedAt: now,
		},
		Spec: model.PackageSpec{
			Version:     manifest.Version,
			Format:      model.PackageFormatTarGz,
			Entrypoint:  manifest.Entrypoint,
			Description: manifest.Description,
			Links:       manifest.Links,
		},
		Status: model.PackageStatus{
			SHA256:    payloadDigest(payload),
			SizeBytes: int64(len(payload)),
			Builtin:   builtin,
		},
	}

	if err := uc.repo.SavePackagePayload(manifest.Name, manifest.Version, payload); err != nil {
		return nil, err
	}

	if err := uc.repo.CreatePackage(p); err != nil {
		return nil, err
	}

	return p, nil
}

// readArchiveManifest scans a tar.gz archive for its root
// manifest.json, validates it and verifies the entrypoint file is
// present.
func readArchiveManifest(payload []byte) (PackageManifest, error) {
	gz, err := gzip.NewReader(bytes.NewReader(payload))
	if err != nil {
		return PackageManifest{}, fmt.Errorf("package is not a gzip archive: %w", err)
	}

	var manifest *PackageManifest
	files := make(map[string]bool)
	tr := tar.NewReader(gz)
	for {
		hdr, err := tr.Next()
		if err == io.EOF {
			break
		}

		if err != nil {
			return PackageManifest{}, fmt.Errorf("package is not a tar archive: %w", err)
		}

		name := filepath.Clean(hdr.Name)
		if hdr.Typeflag != tar.TypeReg || !filepath.IsLocal(name) {
			continue
		}

		files[name] = true
		if name == manifestFileName {
			doc, err := io.ReadAll(io.LimitReader(tr, 1<<20))
			if err != nil {
				return PackageManifest{}, fmt.Errorf("read manifest: %w", err)
			}

			manifest = &PackageManifest{}
			if err := json.Unmarshal(doc, manifest); err != nil {
				return PackageManifest{}, fmt.Errorf("parse %s: %w", manifestFileName, err)
			}
		}
	}

	if manifest == nil {
		return PackageManifest{}, fmt.Errorf("package has no root %s", manifestFileName)
	}

	if err := validateManifest(*manifest, files); err != nil {
		return PackageManifest{}, err
	}

	return *manifest, nil
}

// validateManifest checks the manifest fields against the archive
// content.
func validateManifest(m PackageManifest, files map[string]bool) error {
	if !packageNameRE.MatchString(m.Name) {
		return fmt.Errorf("invalid package name %q (lowercase letters, digits, dashes)", m.Name)
	}

	if !packageVersionRE.MatchString(m.Version) {
		return fmt.Errorf("invalid package version %q", m.Version)
	}

	if m.Entrypoint == "" || !filepath.IsLocal(m.Entrypoint) {
		return fmt.Errorf("invalid entrypoint %q", m.Entrypoint)
	}

	if !files[filepath.Clean(m.Entrypoint)] {
		return fmt.Errorf("entrypoint %q not found in the archive", m.Entrypoint)
	}

	return nil
}

// buildArchive assembles the builtin package archive: the manifest
// plus the executable.
func buildArchive(manifest PackageManifest, binary []byte) ([]byte, error) {
	doc, err := json.Marshal(manifest)
	if err != nil {
		return nil, err
	}

	var buf bytes.Buffer
	gz := gzip.NewWriter(&buf)
	tw := tar.NewWriter(gz)

	entries := []struct {
		name string
		mode int64
		data []byte
	}{
		{name: manifestFileName, mode: 0o644, data: doc},
		{name: manifest.Entrypoint, mode: 0o755, data: binary},
	}

	for _, e := range entries {
		hdr := &tar.Header{Name: e.name, Mode: e.mode, Size: int64(len(e.data))}
		if err := tw.WriteHeader(hdr); err != nil {
			return nil, err
		}

		if _, err := tw.Write(e.data); err != nil {
			return nil, err
		}
	}

	if err := tw.Close(); err != nil {
		return nil, err
	}

	if err := gz.Close(); err != nil {
		return nil, err
	}

	return buf.Bytes(), nil
}

func payloadDigest(payload []byte) string {
	sum := sha256.Sum256(payload)

	return hex.EncodeToString(sum[:])
}

// compareVersions orders dotted versions numerically where possible
// (1.10.0 > 1.9.0) and lexically otherwise; pre-release and build
// suffixes compare as strings. Used for display ordering only —
// program references always name an explicit version.
func compareVersions(a, b string) int {
	as := strings.FieldsFunc(a, func(r rune) bool { return r == '.' || r == '-' || r == '+' })
	bs := strings.FieldsFunc(b, func(r rune) bool { return r == '.' || r == '-' || r == '+' })

	for i := 0; i < len(as) && i < len(bs); i++ {
		an, aerr := strconv.Atoi(as[i])
		bn, berr := strconv.Atoi(bs[i])
		switch {
		case aerr == nil && berr == nil:
			if an != bn {
				return an - bn
			}
		default:
			if c := strings.Compare(as[i], bs[i]); c != 0 {
				return c
			}
		}
	}

	return len(as) - len(bs)
}
