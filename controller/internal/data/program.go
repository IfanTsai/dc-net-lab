package data

import (
	"fmt"
	"time"

	"github.com/ifantsai/dcnetlab/controller/internal/biz"
	"github.com/ifantsai/dcnetlab/internal/model"
)

// NewProgramRepo provides the biz.ProgramRepo implementation.
func NewProgramRepo(d *Data) biz.ProgramRepo { return d }

func (s *Data) CreateProgram(p *model.Program) error {
	doc, err := marshal(p)
	if err != nil {
		return err
	}

	if _, err := s.db.Exec(`INSERT INTO programs (id, lab_id, name, server_name, doc) VALUES (?, ?, ?, ?, ?)`,
		p.Meta.ID, p.Spec.LabID, p.Meta.Name, p.Spec.ServerName, doc); err != nil {
		return fmt.Errorf("insert program: %w", err)
	}

	return nil
}

func (s *Data) UpdateProgram(p *model.Program) error {
	p.Meta.UpdatedAt = time.Now().UTC()
	doc, err := marshal(p)
	if err != nil {
		return err
	}

	res, err := s.db.Exec(`UPDATE programs SET doc = ? WHERE id = ?`, doc, p.Meta.ID)
	if err != nil {
		return fmt.Errorf("update program: %w", err)
	}

	return checkFound(res)
}

func (s *Data) DeleteProgram(id string) error {
	res, err := s.db.Exec(`DELETE FROM programs WHERE id = ?`, id)
	if err != nil {
		return fmt.Errorf("delete program: %w", err)
	}

	return checkFound(res)
}

func (s *Data) GetProgram(id string) (*model.Program, error) {
	return getDoc[model.Program](s, `SELECT doc FROM programs WHERE id = ?`, id)
}

func (s *Data) ListPrograms(labID string) ([]*model.Program, error) {
	return listDocs[model.Program](s, `SELECT doc FROM programs WHERE lab_id = ? ORDER BY name`, labID)
}
