package data

import (
	"fmt"
	"time"

	"github.com/ifantsai/dcnetlab/internal/biz"
	"github.com/ifantsai/dcnetlab/internal/model"
)

// NewCaptureRepo provides the biz.CaptureRepo implementation.
func NewCaptureRepo(d *Data) biz.CaptureRepo { return d }

func (s *Data) CreateCaptureSession(c *model.CaptureSession) error {
	doc, err := marshal(c)
	if err != nil {
		return err
	}

	if _, err := s.db.Exec(`INSERT INTO capture_sessions (id, lab_id, name, doc) VALUES (?, ?, ?, ?)`,
		c.Meta.ID, c.Spec.LabID, c.Meta.Name, doc); err != nil {
		return fmt.Errorf("insert capture session: %w", err)
	}

	return nil
}

func (s *Data) UpdateCaptureSession(c *model.CaptureSession) error {
	c.Meta.UpdatedAt = time.Now().UTC()
	doc, err := marshal(c)
	if err != nil {
		return err
	}

	res, err := s.db.Exec(`UPDATE capture_sessions SET doc = ? WHERE id = ?`, doc, c.Meta.ID)
	if err != nil {
		return fmt.Errorf("update capture session: %w", err)
	}

	return checkFound(res)
}

func (s *Data) DeleteCaptureSession(id string) error {
	res, err := s.db.Exec(`DELETE FROM capture_sessions WHERE id = ?`, id)
	if err != nil {
		return fmt.Errorf("delete capture session: %w", err)
	}

	return checkFound(res)
}

func (s *Data) GetCaptureSession(id string) (*model.CaptureSession, error) {
	return getDoc[model.CaptureSession](s, `SELECT doc FROM capture_sessions WHERE id = ?`, id)
}

func (s *Data) ListCaptureSessions(labID string) ([]*model.CaptureSession, error) {
	return listDocs[model.CaptureSession](s, `SELECT doc FROM capture_sessions WHERE lab_id = ? ORDER BY name`, labID)
}

// ListAllCaptureSessions returns every capture session across labs;
// the capture manager uses it at startup to fail sessions that were
// still running when the controller went down.
func (s *Data) ListAllCaptureSessions() ([]*model.CaptureSession, error) {
	return listDocs[model.CaptureSession](s, `SELECT doc FROM capture_sessions ORDER BY name`)
}
