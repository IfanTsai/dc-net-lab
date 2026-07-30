package biz

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"fmt"
	"io"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/ifantsai/dcnetlab/controller/internal/conf"
	"github.com/ifantsai/dcnetlab/internal/model"
)

// fakePackageRepo keeps packages and payloads in memory.
type fakePackageRepo struct {
	packages map[string]*model.Package
	payloads map[string][]byte
}

func newFakePackageRepo() *fakePackageRepo {
	return &fakePackageRepo{
		packages: make(map[string]*model.Package),
		payloads: make(map[string][]byte),
	}
}

func pkgKey(name, version string) string { return name + "@" + version }

func (r *fakePackageRepo) CreatePackage(p *model.Package) error {
	key := pkgKey(p.Meta.Name, p.Spec.Version)
	if _, ok := r.packages[key]; ok {
		return fmt.Errorf("package %s exists", key)
	}

	r.packages[key] = p

	return nil
}

func (r *fakePackageRepo) UpdatePackage(p *model.Package) error {
	r.packages[pkgKey(p.Meta.Name, p.Spec.Version)] = p

	return nil
}

func (r *fakePackageRepo) DeletePackage(name, version string) error {
	delete(r.packages, pkgKey(name, version))

	return nil
}

func (r *fakePackageRepo) GetPackage(name, version string) (*model.Package, error) {
	p, ok := r.packages[pkgKey(name, version)]
	if !ok {
		return nil, ErrNotFound
	}

	return p, nil
}

func (r *fakePackageRepo) ListPackages() ([]*model.Package, error) {
	out := make([]*model.Package, 0, len(r.packages))
	for _, p := range r.packages {
		out = append(out, p)
	}

	return out, nil
}

func (r *fakePackageRepo) SavePackagePayload(name, version string, payload []byte) error {
	r.payloads[pkgKey(name, version)] = payload

	return nil
}

func (r *fakePackageRepo) PackagePayloadPath(name, version string) (string, error) {
	return "/fake/" + pkgKey(name, version), nil
}

func (r *fakePackageRepo) DeletePackagePayload(name, version string) error {
	delete(r.payloads, pkgKey(name, version))

	return nil
}

func testPackageUsecase(t *testing.T) (*PackageUsecase, *fakePackageRepo) {
	t.Helper()

	repo := newFakePackageRepo()
	log := slog.New(slog.NewTextHandler(io.Discard, nil))
	uc, err := NewPackageUsecase(repo, &conf.Data{}, log)
	if err != nil {
		t.Fatal(err)
	}

	return uc, repo
}

// archiveOf builds a tar.gz with a manifest plus one dummy binary.
func archiveOf(t *testing.T, manifest PackageManifest) []byte {
	t.Helper()

	payload, err := buildArchive(manifest, []byte("#!/bin/sh\n"))
	if err != nil {
		t.Fatal(err)
	}

	return payload
}

func TestUploadPackage(t *testing.T) {
	valid := PackageManifest{Name: "web", Version: "1.0.0", Entrypoint: "run.sh"}

	tests := []struct {
		name    string
		payload func(t *testing.T) []byte
		wantErr string
	}{
		{
			name:    "valid",
			payload: func(t *testing.T) []byte { return archiveOf(t, valid) },
		},
		{
			name:    "empty",
			payload: func(t *testing.T) []byte { return nil },
			wantErr: "empty package",
		},
		{
			name:    "not gzip",
			payload: func(t *testing.T) []byte { return []byte("plain text") },
			wantErr: "not a gzip archive",
		},
		{
			name: "bad name",
			payload: func(t *testing.T) []byte {
				return archiveOf(t, PackageManifest{Name: "Bad_Name", Version: "1.0.0", Entrypoint: "run.sh"})
			},
			wantErr: "invalid package name",
		},
		{
			name: "bad version",
			payload: func(t *testing.T) []byte {
				return archiveOf(t, PackageManifest{Name: "web", Version: "../1", Entrypoint: "run.sh"})
			},
			wantErr: "invalid package version",
		},
		{
			name: "escaping entrypoint",
			payload: func(t *testing.T) []byte {
				return archiveOf(t, PackageManifest{Name: "web", Version: "1.0.0", Entrypoint: "../run.sh"})
			},
			wantErr: "invalid entrypoint",
		},
		{
			name: "reserved builtin name",
			payload: func(t *testing.T) []byte {
				return archiveOf(t, PackageManifest{Name: model.BuiltinPackageName, Version: "1.0.0", Entrypoint: "run.sh"})
			},
			wantErr: "reserved",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			uc, _ := testPackageUsecase(t)

			p, err := uc.UploadPackage(tt.payload(t))
			if tt.wantErr == "" {
				if err != nil {
					t.Fatal(err)
				}

				if p.Meta.Name != "web" || p.Spec.Version != "1.0.0" || p.Status.SHA256 == "" {
					t.Errorf("package = %+v", p)
				}

				return
			}

			if err == nil || !strings.Contains(err.Error(), tt.wantErr) {
				t.Fatalf("err = %v, want %q", err, tt.wantErr)
			}
		})
	}
}

func TestUploadPackageDuplicate(t *testing.T) {
	uc, _ := testPackageUsecase(t)
	payload := archiveOf(t, PackageManifest{Name: "web", Version: "1.0.0", Entrypoint: "run.sh"})

	if _, err := uc.UploadPackage(payload); err != nil {
		t.Fatal(err)
	}

	if _, err := uc.UploadPackage(payload); err == nil || !strings.Contains(err.Error(), "already exists") {
		t.Fatalf("duplicate upload: err = %v", err)
	}
}

func TestUploadPackageMissingEntrypoint(t *testing.T) {
	uc, _ := testPackageUsecase(t)

	// Build an inconsistent archive by hand: the manifest declares
	// other.sh, the archive only carries run.sh.
	var buf bytes.Buffer
	gz := gzip.NewWriter(&buf)
	tw := tar.NewWriter(gz)
	files := map[string]string{
		manifestFileName: `{"name":"web","version":"1.0.0","entrypoint":"other.sh"}`,
		"run.sh":         "#!/bin/sh\n",
	}

	for name, content := range files {
		hdr := &tar.Header{Name: name, Mode: 0o644, Size: int64(len(content))}
		if err := tw.WriteHeader(hdr); err != nil {
			t.Fatal(err)
		}

		if _, err := tw.Write([]byte(content)); err != nil {
			t.Fatal(err)
		}
	}

	if err := tw.Close(); err != nil {
		t.Fatal(err)
	}

	if err := gz.Close(); err != nil {
		t.Fatal(err)
	}

	if _, err := uc.UploadPackage(buf.Bytes()); err == nil || !strings.Contains(err.Error(), "not found in the archive") {
		t.Fatalf("upload with missing entrypoint: err = %v", err)
	}
}

func TestRegisterBuiltin(t *testing.T) {
	binDir := t.TempDir()
	binary := filepath.Join(binDir, model.BuiltinPackageEntrypoint)
	if err := os.WriteFile(binary, []byte("v1"), 0o755); err != nil {
		t.Fatal(err)
	}

	repo := newFakePackageRepo()
	log := slog.New(slog.NewTextHandler(io.Discard, nil))
	if _, err := NewPackageUsecase(repo, &conf.Data{BinDir: binDir}, log); err != nil {
		t.Fatal(err)
	}

	p, err := repo.GetPackage(model.BuiltinPackageName, model.BuiltinPackageVersion)
	if err != nil {
		t.Fatal(err)
	}

	if !p.Status.Builtin || p.Spec.Entrypoint != model.BuiltinPackageEntrypoint {
		t.Errorf("builtin package = %+v", p)
	}

	// A rebuilt binary behind the same version replaces the payload.
	firstSHA := p.Status.SHA256
	if err := os.WriteFile(binary, []byte("v2"), 0o755); err != nil {
		t.Fatal(err)
	}

	if _, err := NewPackageUsecase(repo, &conf.Data{BinDir: binDir}, log); err != nil {
		t.Fatal(err)
	}

	p, err = repo.GetPackage(model.BuiltinPackageName, model.BuiltinPackageVersion)
	if err != nil {
		t.Fatal(err)
	}

	if p.Status.SHA256 == firstSHA {
		t.Error("payload not replaced after binary change")
	}
}

func TestDeletePackageProtectsBuiltin(t *testing.T) {
	uc, repo := testPackageUsecase(t)

	builtin := &model.Package{
		Meta:   model.ResourceMeta{ID: "pkg-1", Name: model.BuiltinPackageName},
		Spec:   model.PackageSpec{Version: model.BuiltinPackageVersion},
		Status: model.PackageStatus{Builtin: true},
	}

	if err := repo.CreatePackage(builtin); err != nil {
		t.Fatal(err)
	}

	if err := uc.DeletePackage(model.BuiltinPackageName, model.BuiltinPackageVersion); err == nil {
		t.Fatal("builtin deletion not rejected")
	}

	if _, err := uc.UploadPackage(archiveOf(t, PackageManifest{Name: "web", Version: "1.0.0", Entrypoint: "run.sh"})); err != nil {
		t.Fatal(err)
	}

	if err := uc.DeletePackage("web", "1.0.0"); err != nil {
		t.Fatal(err)
	}

	if _, err := uc.GetPackage("web", "1.0.0"); err == nil {
		t.Fatal("package still present after delete")
	}
}

func TestCompareVersions(t *testing.T) {
	tests := []struct {
		a, b string
		want int // sign only
	}{
		{"1.0.0", "1.0.0", 0},
		{"1.10.0", "1.9.0", 1},
		{"1.2.3", "1.2.4", -1},
		{"2.0.0", "1.99.99", 1},
		{"1.0.0", "1.0", 1},
		{"1.0.0-alpha", "1.0.0-beta", -1},
	}

	for _, tt := range tests {
		t.Run(tt.a+" vs "+tt.b, func(t *testing.T) {
			got := compareVersions(tt.a, tt.b)
			switch {
			case tt.want == 0 && got != 0,
				tt.want > 0 && got <= 0,
				tt.want < 0 && got >= 0:
				t.Errorf("compareVersions(%q, %q) = %d, want sign %d", tt.a, tt.b, got, tt.want)
			}
		})
	}
}
