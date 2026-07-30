package data

import (
	"fmt"
	"time"

	"github.com/ifantsai/dcnetlab/controller/internal/biz"
	"github.com/ifantsai/dcnetlab/internal/model"
)

// NewTrafficRepo provides the biz.TrafficRepo implementation.
func NewTrafficRepo(d *Data) biz.TrafficRepo { return d }

func (s *Data) CreateTrafficScenario(sc *model.TrafficScenario) error {
	doc, err := marshal(sc)
	if err != nil {
		return err
	}

	if _, err := s.db.Exec(`INSERT INTO traffic_scenarios (id, lab_id, name, doc) VALUES (?, ?, ?, ?)`,
		sc.Meta.ID, sc.Spec.LabID, sc.Meta.Name, doc); err != nil {
		return fmt.Errorf("insert traffic scenario: %w", err)
	}

	return nil
}

func (s *Data) UpdateTrafficScenario(sc *model.TrafficScenario) error {
	sc.Meta.UpdatedAt = time.Now().UTC()
	doc, err := marshal(sc)
	if err != nil {
		return err
	}

	res, err := s.db.Exec(`UPDATE traffic_scenarios SET doc = ? WHERE id = ?`, doc, sc.Meta.ID)
	if err != nil {
		return fmt.Errorf("update traffic scenario: %w", err)
	}

	return checkFound(res)
}

func (s *Data) DeleteTrafficScenario(id string) error {
	res, err := s.db.Exec(`DELETE FROM traffic_scenarios WHERE id = ?`, id)
	if err != nil {
		return fmt.Errorf("delete traffic scenario: %w", err)
	}

	return checkFound(res)
}

func (s *Data) GetTrafficScenario(id string) (*model.TrafficScenario, error) {
	return getDoc[model.TrafficScenario](s, `SELECT doc FROM traffic_scenarios WHERE id = ?`, id)
}

func (s *Data) ListTrafficScenarios(labID string) ([]*model.TrafficScenario, error) {
	return listDocs[model.TrafficScenario](s, `SELECT doc FROM traffic_scenarios WHERE lab_id = ? ORDER BY name`, labID)
}
