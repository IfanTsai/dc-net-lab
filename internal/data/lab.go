package data

import (
	"database/sql"
	"fmt"
	"time"

	"github.com/ifantsai/dcnetlab/internal/biz"
	"github.com/ifantsai/dcnetlab/internal/metrics"
	"github.com/ifantsai/dcnetlab/internal/model"
	"github.com/ifantsai/dcnetlab/internal/observer"
	"github.com/ifantsai/dcnetlab/internal/traffic"
)

// NewLabRepo provides the biz.LabRepo implementation.
func NewLabRepo(d *Data) biz.LabRepo { return d }

// NewPowerRepo exposes the lab/node persistence the power usecase needs.
func NewPowerRepo(d *Data) biz.PowerRepo { return d }

// NewObserverStore exposes the persistence the observer needs.
func NewObserverStore(d *Data) observer.Store { return d }

// NewMetricsStore exposes the persistence the metrics collector needs.
func NewMetricsStore(d *Data) metrics.Store { return d }

// NewTrafficStore exposes the persistence the traffic collector needs.
func NewTrafficStore(d *Data) traffic.Store { return d }

func (s *Data) CreateLab(lab *model.Lab) error {
	doc, err := marshal(lab)
	if err != nil {
		return err
	}

	if _, err := s.db.Exec(`INSERT INTO labs (id, name, doc) VALUES (?, ?, ?)`, lab.Meta.ID, lab.Meta.Name, doc); err != nil {
		return fmt.Errorf("insert lab: %w", err)
	}

	return nil
}

func (s *Data) UpdateLab(lab *model.Lab) error {
	lab.Meta.UpdatedAt = time.Now().UTC()
	doc, err := marshal(lab)
	if err != nil {
		return err
	}

	res, err := s.db.Exec(`UPDATE labs SET doc = ?, name = ? WHERE id = ?`, doc, lab.Meta.Name, lab.Meta.ID)
	if err != nil {
		return fmt.Errorf("update lab: %w", err)
	}

	return checkFound(res)
}

func (s *Data) GetLab(id string) (*model.Lab, error) {
	return getDoc[model.Lab](s, `SELECT doc FROM labs WHERE id = ?`, id)
}

func (s *Data) ListLabs() ([]*model.Lab, error) {
	return listDocs[model.Lab](s, `SELECT doc FROM labs ORDER BY name`)
}

// DeleteLab removes the lab and everything owned by it.
func (s *Data) DeleteLab(id string) error {
	return s.tx(func(tx *sql.Tx) error {
		for _, q := range []string{
			`DELETE FROM nodes WHERE lab_id = ?`,
			`DELETE FROM links WHERE lab_id = ?`,
			`DELETE FROM plans WHERE lab_id = ?`,
			`DELETE FROM generations WHERE lab_id = ?`,
			`DELETE FROM allocations WHERE lab_id = ?`,
			`DELETE FROM programs WHERE lab_id = ?`,
			`DELETE FROM labs WHERE id = ?`,
		} {
			if _, err := tx.Exec(q, id); err != nil {
				return err
			}
		}

		return nil
	})
}
