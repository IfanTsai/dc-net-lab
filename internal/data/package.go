package data

import (
	"fmt"
	"os"
	"path/filepath"
	"time"

	"github.com/ifantsai/dcnetlab/internal/biz"
	"github.com/ifantsai/dcnetlab/internal/model"
)

// NewPackageRepo provides the biz.PackageRepo implementation.
func NewPackageRepo(d *Data) biz.PackageRepo { return d }

func (s *Data) CreatePackage(p *model.Package) error {
	doc, err := marshal(p)
	if err != nil {
		return err
	}

	if _, err := s.db.Exec(`INSERT INTO packages (id, name, version, doc) VALUES (?, ?, ?, ?)`,
		p.Meta.ID, p.Meta.Name, p.Spec.Version, doc); err != nil {
		return fmt.Errorf("insert package: %w", err)
	}

	return nil
}

func (s *Data) UpdatePackage(p *model.Package) error {
	p.Meta.UpdatedAt = time.Now().UTC()
	doc, err := marshal(p)
	if err != nil {
		return err
	}

	res, err := s.db.Exec(`UPDATE packages SET doc = ? WHERE id = ?`, doc, p.Meta.ID)
	if err != nil {
		return fmt.Errorf("update package: %w", err)
	}

	return checkFound(res)
}

func (s *Data) DeletePackage(name, version string) error {
	res, err := s.db.Exec(`DELETE FROM packages WHERE name = ? AND version = ?`, name, version)
	if err != nil {
		return fmt.Errorf("delete package: %w", err)
	}

	return checkFound(res)
}

func (s *Data) GetPackage(name, version string) (*model.Package, error) {
	return getDoc[model.Package](s, `SELECT doc FROM packages WHERE name = ? AND version = ?`, name, version)
}

func (s *Data) ListPackages() ([]*model.Package, error) {
	return listDocs[model.Package](s, `SELECT doc FROM packages ORDER BY name, version`)
}

// SavePackagePayload stores a package archive on disk next to the
// database; the repo HTTP endpoint serves it from there.
func (s *Data) SavePackagePayload(name, version string, payload []byte) error {
	path, err := s.payloadPath(name, version)
	if err != nil {
		return err
	}

	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return fmt.Errorf("create package dir: %w", err)
	}

	if err := os.WriteFile(path, payload, 0o644); err != nil {
		return fmt.Errorf("write package payload: %w", err)
	}

	return nil
}

// PackagePayloadPath returns where a package archive lives on disk.
func (s *Data) PackagePayloadPath(name, version string) (string, error) {
	return s.payloadPath(name, version)
}

// DeletePackagePayload removes the stored archive; a missing file is
// not an error (payload and row deletion must be idempotent).
func (s *Data) DeletePackagePayload(name, version string) error {
	path, err := s.payloadPath(name, version)
	if err != nil {
		return err
	}

	if err := os.Remove(path); err != nil && !os.IsNotExist(err) {
		return fmt.Errorf("remove package payload: %w", err)
	}

	return nil
}

// payloadPath builds <dataDir>/packages/<name>/<version>.tar.gz,
// refusing path components that would escape it. Names and versions
// are validated in biz; this is defence in depth.
func (s *Data) payloadPath(name, version string) (string, error) {
	rel := filepath.Join("packages", name, version+".tar.gz")
	if !filepath.IsLocal(rel) {
		return "", fmt.Errorf("invalid package path %s@%s", name, version)
	}

	return filepath.Join(s.dir, rel), nil
}
