// Package operation runs asynchronous changes. Every write API call
// creates an Operation the UI can follow; steps and errors are
// persisted so progress survives a controller restart.
package operation

import (
	"context"
	"fmt"
	"log/slog"
	"time"

	"github.com/ifantsai/dcnetlab/internal/model"
)

// Store persists operations; the data layer provides the
// implementation.
type Store interface {
	CreateOperation(op *model.Operation) error
	UpdateOperation(op *model.Operation) error
}

// Step is one named unit of work inside an operation.
type Step struct {
	Name string
	Fn   func(ctx context.Context) error
}

// Manager creates and executes operations.
type Manager struct {
	store   Store
	log     *slog.Logger
	timeout time.Duration
}

// NewManager creates a manager. timeout bounds one operation run.
func NewManager(st Store, log *slog.Logger, timeout time.Duration) *Manager {
	if timeout <= 0 {
		timeout = 10 * time.Minute
	}

	return &Manager{store: st, log: log, timeout: timeout}
}

// Create persists a new queued operation.
func (m *Manager) Create(labID string, typ model.OperationType, res model.ResourceRef) (*model.Operation, error) {
	op := &model.Operation{
		ID:        model.NewID("op"),
		LabID:     labID,
		Type:      typ,
		Resource:  res,
		State:     model.OperationQueued,
		CreatedAt: time.Now().UTC(),
		UpdatedAt: time.Now().UTC(),
	}

	if err := m.store.CreateOperation(op); err != nil {
		return nil, fmt.Errorf("persist operation: %w", err)
	}

	return op, nil
}

// Run executes steps asynchronously, persisting progress after every
// step. onDone (optional) runs after the final state is stored.
func (m *Manager) Run(op *model.Operation, steps []Step, onDone func(failed error)) {
	op.Steps = make([]model.OperationStep, len(steps))
	for i, s := range steps {
		op.Steps[i] = model.OperationStep{Name: s.Name, State: model.OperationQueued}
	}

	op.State = model.OperationRunning
	m.persist(op)

	go func() {
		ctx, cancel := context.WithTimeout(context.Background(), m.timeout)
		defer cancel()

		var failed error
		for i, s := range steps {
			now := time.Now().UTC()
			op.Steps[i].State = model.OperationRunning
			op.Steps[i].StartedAt = &now
			m.persist(op)

			err := s.Fn(ctx)
			end := time.Now().UTC()
			op.Steps[i].FinishedAt = &end
			if err != nil {
				op.Steps[i].State = model.OperationFailed
				op.Steps[i].Message = err.Error()
				failed = err
				m.log.Error("operation step failed",
					"operation_id", op.ID, "step", s.Name, "error", err)
				m.persist(op)
				break
			}

			op.Steps[i].State = model.OperationSucceeded
			op.Progress = (i + 1) * 100 / len(steps)
			m.persist(op)
		}

		if failed != nil {
			op.State = model.OperationFailed
			op.Error = &model.OperationError{Code: "STEP_FAILED", Message: failed.Error()}
		} else {
			op.State = model.OperationSucceeded
			op.Progress = 100
		}

		// Finalise dependent resources (lab phase, plan state) before
		// the operation is reported done, so a client that polls the
		// operation and then reads the lab sees consistent state.
		if onDone != nil {
			onDone(failed)
		}

		m.persist(op)
	}()
}

func (m *Manager) persist(op *model.Operation) {
	if err := m.store.UpdateOperation(op); err != nil {
		m.log.Error("persist operation", "operation_id", op.ID, "error", err)
	}
}
