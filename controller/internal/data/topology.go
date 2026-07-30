package data

import (
	"database/sql"
	"fmt"
	"time"

	"github.com/ifantsai/dcnetlab/controller/internal/biz"
	"github.com/ifantsai/dcnetlab/internal/model"
)

// NewTopologyRepo provides the biz.TopologyRepo implementation.
func NewTopologyRepo(d *Data) biz.TopologyRepo { return d }

// ReplaceTopology atomically replaces all nodes, links and allocations
// of a lab, used when a new generation is planned.
func (s *Data) ReplaceTopology(labID string, nodes []*model.Node, links []*model.Link, allocs []model.Allocation) error {
	return s.tx(func(tx *sql.Tx) error {
		for _, q := range []string{
			`DELETE FROM nodes WHERE lab_id = ?`,
			`DELETE FROM links WHERE lab_id = ?`,
			`DELETE FROM allocations WHERE lab_id = ?`,
		} {
			if _, err := tx.Exec(q, labID); err != nil {
				return err
			}
		}

		for _, n := range nodes {
			doc, err := marshal(n)
			if err != nil {
				return err
			}

			if _, err := tx.Exec(`INSERT INTO nodes (id, lab_id, name, doc) VALUES (?, ?, ?, ?)`,
				n.Meta.ID, labID, n.Meta.Name, doc); err != nil {
				return err
			}
		}

		for _, l := range links {
			doc, err := marshal(l)
			if err != nil {
				return err
			}

			if _, err := tx.Exec(`INSERT INTO links (id, lab_id, name, doc) VALUES (?, ?, ?, ?)`,
				l.Meta.ID, labID, l.Meta.Name, doc); err != nil {
				return err
			}
		}

		for _, a := range allocs {
			if _, err := tx.Exec(`INSERT INTO allocations (lab_id, pool, value, owner) VALUES (?, ?, ?, ?)`,
				labID, a.Pool, a.Value, a.Owner); err != nil {
				return err
			}
		}

		return nil
	})
}

func (s *Data) ListNodes(labID string) ([]*model.Node, error) {
	return listDocs[model.Node](s, `SELECT doc FROM nodes WHERE lab_id = ? ORDER BY name`, labID)
}

func (s *Data) GetNode(id string) (*model.Node, error) {
	return getDoc[model.Node](s, `SELECT doc FROM nodes WHERE id = ?`, id)
}

func (s *Data) UpdateNode(n *model.Node) error {
	n.Meta.UpdatedAt = time.Now().UTC()
	doc, err := marshal(n)
	if err != nil {
		return err
	}

	res, err := s.db.Exec(`UPDATE nodes SET doc = ? WHERE id = ?`, doc, n.Meta.ID)
	if err != nil {
		return fmt.Errorf("update node: %w", err)
	}

	return checkFound(res)
}

func (s *Data) ListLinks(labID string) ([]*model.Link, error) {
	return listDocs[model.Link](s, `SELECT doc FROM links WHERE lab_id = ? ORDER BY name`, labID)
}

func (s *Data) ListAllocations(labID string) ([]model.Allocation, error) {
	rows, err := s.db.Query(`SELECT pool, value, owner FROM allocations WHERE lab_id = ? ORDER BY pool, value`, labID)
	if err != nil {
		return nil, err
	}

	defer rows.Close()

	var out []model.Allocation
	for rows.Next() {
		var a model.Allocation
		if err := rows.Scan(&a.Pool, &a.Value, &a.Owner); err != nil {
			return nil, err
		}

		out = append(out, a)
	}

	return out, rows.Err()
}
