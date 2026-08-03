package operation

import (
	"context"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"sync"
	"testing"
	"time"

	"github.com/ifantsai/dcnetlab/internal/model"
)

func testLogger() *slog.Logger {
	return slog.New(slog.NewTextHandler(io.Discard, nil))
}

// memStore keeps operations in memory; failUpdate simulates the row
// vanishing under a cascading lab delete.
type memStore struct {
	mu         sync.Mutex
	ops        map[string]*model.Operation
	failUpdate bool
}

func newMemStore() *memStore { return &memStore{ops: make(map[string]*model.Operation)} }

func (s *memStore) CreateOperation(op *model.Operation) error {
	s.mu.Lock()
	defer s.mu.Unlock()

	s.ops[op.ID] = op.Clone()

	return nil
}

func (s *memStore) UpdateOperation(op *model.Operation) error {
	s.mu.Lock()
	defer s.mu.Unlock()

	if s.failUpdate {
		return errors.New("row gone")
	}

	s.ops[op.ID] = op.Clone()

	return nil
}

// collect reads frames until the operation reaches a terminal state
// or the timeout expires.
func collect(t *testing.T, updates <-chan *model.Operation) []*model.Operation {
	t.Helper()

	var frames []*model.Operation
	deadline := time.After(5 * time.Second)
	for {
		select {
		case op := <-updates:
			frames = append(frames, op)
			if op.State == model.OperationSucceeded || op.State == model.OperationFailed {
				return frames
			}
		case <-deadline:
			t.Fatalf("no terminal frame within timeout; got %d frames", len(frames))
		}
	}
}

func TestSubscribeStreamsLifecycle(t *testing.T) {
	m := NewManager(newMemStore(), testLogger(), time.Minute)

	updates, cancel := m.Subscribe("lab-1")
	defer cancel()

	op, err := m.Create("lab-1", model.OperationApplyPlan, model.ResourceRef{Type: "plan", ID: "p-1"})
	if err != nil {
		t.Fatal(err)
	}

	m.Run(op, []Step{
		{Name: "one", Fn: func(ctx context.Context) error {
			Report(ctx, 0.5)

			return nil
		}},
		{Name: "two", Fn: func(context.Context) error { return nil }},
	}, nil)

	frames := collect(t, updates)

	if frames[0].State != model.OperationQueued {
		t.Errorf("first frame state = %s, want Queued", frames[0].State)
	}

	last := frames[len(frames)-1]
	if last.State != model.OperationSucceeded || last.Progress != 100 {
		t.Errorf("terminal frame = %s/%d, want Succeeded/100", last.State, last.Progress)
	}

	if len(last.Steps) != 2 || last.Steps[1].State != model.OperationSucceeded {
		t.Errorf("terminal frame steps = %+v, want both Succeeded", last.Steps)
	}

	progress := -1
	for _, f := range frames {
		if f.LabID != "lab-1" {
			t.Errorf("frame for lab %s leaked into lab-1 subscription", f.LabID)
		}

		if f.Progress < progress {
			t.Errorf("progress went backwards: %d after %d", f.Progress, progress)
		}

		progress = f.Progress
	}
}

func TestSubscribeFailedStepPublishesTerminalFrame(t *testing.T) {
	m := NewManager(newMemStore(), testLogger(), time.Minute)

	updates, cancel := m.Subscribe("lab-1")
	defer cancel()

	op, err := m.Create("lab-1", model.OperationApplyPlan, model.ResourceRef{Type: "plan", ID: "p-1"})
	if err != nil {
		t.Fatal(err)
	}

	m.Run(op, []Step{
		{Name: "boom", Fn: func(context.Context) error { return errors.New("kaput") }},
	}, nil)

	frames := collect(t, updates)
	last := frames[len(frames)-1]

	if last.State != model.OperationFailed || last.Error == nil || last.Error.Message != "kaput" {
		t.Errorf("terminal frame = %+v, want Failed with error kaput", last)
	}
}

func TestSubscribePublishesDespiteStoreFailure(t *testing.T) {
	st := newMemStore()
	m := NewManager(st, testLogger(), time.Minute)

	op, err := m.Create("lab-1", model.OperationDestroyLab, model.ResourceRef{Type: "lab", ID: "lab-1"})
	if err != nil {
		t.Fatal(err)
	}

	updates, cancel := m.Subscribe("lab-1")
	defer cancel()

	// The lab cascade deleted the row: every update fails, but the
	// subscriber still sees the terminal frame.
	st.failUpdate = true
	m.Run(op, []Step{{Name: "one", Fn: func(context.Context) error { return nil }}}, nil)

	frames := collect(t, updates)
	if frames[len(frames)-1].State != model.OperationSucceeded {
		t.Errorf("terminal state = %s, want Succeeded", frames[len(frames)-1].State)
	}
}

func TestSubscribeCancelStopsDelivery(t *testing.T) {
	m := NewManager(newMemStore(), testLogger(), time.Minute)

	updates, cancel := m.Subscribe("lab-1")
	cancel()

	if _, err := m.Create("lab-1", model.OperationApplyPlan, model.ResourceRef{}); err != nil {
		t.Fatal(err)
	}

	select {
	case <-updates:
		t.Error("no frame expected after cancel")
	default:
	}
}

func TestSlowConsumerKeepsNewestFrame(t *testing.T) {
	m := NewManager(newMemStore(), testLogger(), time.Minute)

	updates, cancel := m.Subscribe("lab-1")
	defer cancel()

	// Publish far more updates than the subscriber buffer holds
	// without reading any: old frames must be dropped, never new ones.
	var last string
	for i := 0; i < 20; i++ {
		op, err := m.Create("lab-1", model.OperationApplyPlan, model.ResourceRef{Type: "plan", ID: fmt.Sprintf("p-%d", i)})
		if err != nil {
			t.Fatal(err)
		}

		last = op.ID
	}

	var newest *model.Operation
	for {
		select {
		case op := <-updates:
			newest = op

			continue
		default:
		}

		break
	}

	if newest == nil || newest.ID != last {
		t.Errorf("newest drained frame = %+v, want operation %s", newest, last)
	}
}

func TestPublishedFrameIsIsolatedFromExecutor(t *testing.T) {
	m := NewManager(newMemStore(), testLogger(), time.Minute)

	updates, cancel := m.Subscribe("lab-1")
	defer cancel()

	op, err := m.Create("lab-1", model.OperationApplyPlan, model.ResourceRef{Type: "plan", ID: "p-1"})
	if err != nil {
		t.Fatal(err)
	}

	frame := <-updates

	// Mutate the executor's copy; the published frame must not move.
	op.State = model.OperationFailed
	op.Steps = append(op.Steps, model.OperationStep{Name: "late"})

	if frame.State != model.OperationQueued || len(frame.Steps) != 0 {
		t.Errorf("published frame changed under the executor's hands: %+v", frame)
	}
}
