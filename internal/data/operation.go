package data

import (
	"fmt"
	"time"

	"github.com/ifantsai/dcnetlab/internal/biz"
	"github.com/ifantsai/dcnetlab/internal/model"
	"github.com/ifantsai/dcnetlab/internal/operation"
)

// NewOperationRepo provides the biz.OperationRepo implementation.
func NewOperationRepo(d *Data) biz.OperationRepo { return d }

// NewOperationStore provides the persistence for the async operation
// executor.
func NewOperationStore(d *Data) operation.Store { return d }

func (s *Data) CreateOperation(op *model.Operation) error {
	doc, err := marshal(op)
	if err != nil {
		return err
	}

	if _, err := s.db.Exec(`INSERT INTO operations (id, lab_id, created_at, doc) VALUES (?, ?, ?, ?)`,
		op.ID, op.LabID, op.CreatedAt.UTC().Format(time.RFC3339Nano), doc); err != nil {
		return fmt.Errorf("insert operation: %w", err)
	}

	return nil
}

func (s *Data) UpdateOperation(op *model.Operation) error {
	op.UpdatedAt = time.Now().UTC()
	doc, err := marshal(op)
	if err != nil {
		return err
	}

	res, err := s.db.Exec(`UPDATE operations SET doc = ? WHERE id = ?`, doc, op.ID)
	if err != nil {
		return fmt.Errorf("update operation: %w", err)
	}

	return checkFound(res)
}

func (s *Data) GetOperation(id string) (*model.Operation, error) {
	return getDoc[model.Operation](s, `SELECT doc FROM operations WHERE id = ?`, id)
}

func (s *Data) ListOperations(labID string) ([]*model.Operation, error) {
	return listDocs[model.Operation](s, `SELECT doc FROM operations WHERE lab_id = ? ORDER BY created_at DESC`, labID)
}
