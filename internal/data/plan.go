package data

import (
	"fmt"
	"time"

	"github.com/ifantsai/dcnetlab/internal/biz"
	"github.com/ifantsai/dcnetlab/internal/model"
)

// NewPlanRepo provides the biz.PlanRepo implementation.
func NewPlanRepo(d *Data) biz.PlanRepo { return d }

func (s *Data) CreatePlan(p *model.Plan) error {
	doc, err := marshal(p)
	if err != nil {
		return err
	}

	if _, err := s.db.Exec(`INSERT INTO plans (id, lab_id, doc) VALUES (?, ?, ?)`, p.ID, p.LabID, doc); err != nil {
		return fmt.Errorf("insert plan: %w", err)
	}

	return nil
}

func (s *Data) UpdatePlan(p *model.Plan) error {
	doc, err := marshal(p)
	if err != nil {
		return err
	}

	res, err := s.db.Exec(`UPDATE plans SET doc = ? WHERE id = ?`, doc, p.ID)
	if err != nil {
		return fmt.Errorf("update plan: %w", err)
	}

	return checkFound(res)
}

func (s *Data) GetPlan(id string) (*model.Plan, error) {
	return getDoc[model.Plan](s, `SELECT doc FROM plans WHERE id = ?`, id)
}

// --- Generations ---

func (s *Data) SaveGeneration(labID string, generation int64, snap *biz.DesiredStateSnapshot) error {
	doc, err := marshal(snap)
	if err != nil {
		return err
	}

	_, err = s.db.Exec(`INSERT OR REPLACE INTO generations (lab_id, generation, created_at, desired_state) VALUES (?, ?, ?, ?)`,
		labID, generation, time.Now().UTC().Format(time.RFC3339Nano), doc)
	if err != nil {
		return fmt.Errorf("insert generation: %w", err)
	}

	// Retain only the most recent 10 generations, per the PRD.
	if _, err := s.db.Exec(`DELETE FROM generations WHERE lab_id = ? AND generation <= (
		SELECT generation FROM generations WHERE lab_id = ? ORDER BY generation DESC LIMIT 1 OFFSET 10)`,
		labID, labID); err != nil {
		return fmt.Errorf("prune generations: %w", err)
	}

	return nil
}

func (s *Data) GetGeneration(labID string, generation int64) (*biz.DesiredStateSnapshot, error) {
	return getDoc[biz.DesiredStateSnapshot](s, `SELECT desired_state FROM generations WHERE lab_id = ? AND generation = ?`, labID, generation)
}

func (s *Data) ListGenerations(labID string) ([]int64, error) {
	rows, err := s.db.Query(`SELECT generation FROM generations WHERE lab_id = ? ORDER BY generation DESC`, labID)
	if err != nil {
		return nil, err
	}

	defer rows.Close()

	var out []int64
	for rows.Next() {
		var g int64
		if err := rows.Scan(&g); err != nil {
			return nil, err
		}

		out = append(out, g)
	}

	return out, rows.Err()
}
