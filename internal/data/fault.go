package data

import (
	"fmt"
	"time"

	"github.com/ifantsai/dcnetlab/internal/biz"
	"github.com/ifantsai/dcnetlab/internal/model"
)

// NewFaultRepo provides the biz.FaultRepo implementation.
func NewFaultRepo(d *Data) biz.FaultRepo { return d }

func (s *Data) CreateFaultScenario(sc *model.FaultScenario) error {
	doc, err := marshal(sc)
	if err != nil {
		return err
	}

	if _, err := s.db.Exec(`INSERT INTO fault_scenarios (id, lab_id, name, doc) VALUES (?, ?, ?, ?)`,
		sc.Meta.ID, sc.Spec.LabID, sc.Meta.Name, doc); err != nil {
		return fmt.Errorf("insert fault scenario: %w", err)
	}

	return nil
}

func (s *Data) UpdateFaultScenario(sc *model.FaultScenario) error {
	sc.Meta.UpdatedAt = time.Now().UTC()
	doc, err := marshal(sc)
	if err != nil {
		return err
	}

	res, err := s.db.Exec(`UPDATE fault_scenarios SET doc = ? WHERE id = ?`, doc, sc.Meta.ID)
	if err != nil {
		return fmt.Errorf("update fault scenario: %w", err)
	}

	return checkFound(res)
}

func (s *Data) DeleteFaultScenario(id string) error {
	res, err := s.db.Exec(`DELETE FROM fault_scenarios WHERE id = ?`, id)
	if err != nil {
		return fmt.Errorf("delete fault scenario: %w", err)
	}

	return checkFound(res)
}

func (s *Data) GetFaultScenario(id string) (*model.FaultScenario, error) {
	return getDoc[model.FaultScenario](s, `SELECT doc FROM fault_scenarios WHERE id = ?`, id)
}

func (s *Data) ListFaultScenarios(labID string) ([]*model.FaultScenario, error) {
	return listDocs[model.FaultScenario](s, `SELECT doc FROM fault_scenarios WHERE lab_id = ? ORDER BY name`, labID)
}
