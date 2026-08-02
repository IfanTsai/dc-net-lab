// Package operation runs asynchronous changes. Every write API call
// creates an Operation the UI can follow; steps and errors are
// persisted so progress survives a controller restart.
package operation

import (
	"context"
	"fmt"
	"log/slog"
	"sync"
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
	// Weight is the step's share of the progress bar, relative to the
	// other steps' weights (zero means 1). Progress advances by
	// completed weight, so a step that deploys containers can be worth
	// ten bookkeeping steps and the bar tracks wall-clock reality
	// instead of jumping at step boundaries.
	Weight int
}

func (s Step) weight() int {
	if s.Weight <= 0 {
		return 1
	}

	return s.Weight
}

// reporterKey carries a step's sub-progress callback in its context.
type reporterKey struct{}

// Report publishes a running step's own progress as a fraction in
// [0, 1]. Steps that loop over many items call it so the operation's
// progress moves inside the step instead of stalling until the step
// finishes. Outside a managed step it is a no-op, and reported
// progress only ever moves the bar forward.
func Report(ctx context.Context, frac float64) {
	if fn, ok := ctx.Value(reporterKey{}).(func(float64)); ok {
		fn(frac)
	}
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
	total := 0
	for i, s := range steps {
		op.Steps[i] = model.OperationStep{Name: s.Name, State: model.OperationQueued, Weight: s.weight()}
		total += s.weight()
	}

	op.State = model.OperationRunning
	m.persist(op)

	go func() {
		ctx, cancel := context.WithTimeout(context.Background(), m.timeout)
		defer cancel()

		var failed error
		done := 0
		for i, s := range steps {
			now := time.Now().UTC()
			op.Steps[i].State = model.OperationRunning
			op.Steps[i].StartedAt = &now
			m.persist(op)

			err := s.Fn(m.withReporter(ctx, op, done, s.weight(), total))
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
			done += s.weight()
			op.Progress = done * 100 / total
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

// withReporter arms the context with a Report callback that maps the
// running step's own fraction onto the operation-wide progress. It
// persists only when the integer percentage moves forward, so
// chatty reporters (one call per pinged server) stay cheap, and a
// concurrent step may report from several goroutines.
func (m *Manager) withReporter(ctx context.Context, op *model.Operation, done, weight, total int) context.Context {
	var mu sync.Mutex

	return context.WithValue(ctx, reporterKey{}, func(frac float64) {
		if frac < 0 {
			frac = 0
		}

		if frac > 1 {
			frac = 1
		}

		mu.Lock()
		defer mu.Unlock()

		p := (done*100 + int(frac*float64(weight*100))) / total
		if p <= op.Progress {
			return
		}

		op.Progress = p
		m.persist(op)
	})
}

func (m *Manager) persist(op *model.Operation) {
	if err := m.store.UpdateOperation(op); err != nil {
		m.log.Error("persist operation", "operation_id", op.ID, "error", err)
	}
}
