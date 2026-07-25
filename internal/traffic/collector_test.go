package traffic

import (
	"context"
	"io"
	"log/slog"
	"testing"
	"time"

	"github.com/ifantsai/dcnetlab/internal/model"
	"github.com/ifantsai/dcnetlab/internal/runtime"
)

func testLogger() *slog.Logger {
	return slog.New(slog.NewTextHandler(io.Discard, nil))
}

// fakeStore serves labs and their scenarios and records status
// updates.
type fakeStore struct {
	labs      []*model.Lab
	scenarios map[string][]*model.TrafficScenario // labID -> scenarios
	updates   []string                            // scenario IDs updated
}

func (s *fakeStore) ListLabs() ([]*model.Lab, error) { return s.labs, nil }

func (s *fakeStore) ListTrafficScenarios(labID string) ([]*model.TrafficScenario, error) {
	return s.scenarios[labID], nil
}

func (s *fakeStore) UpdateTrafficScenario(sc *model.TrafficScenario) error {
	s.updates = append(s.updates, sc.Meta.ID)

	return nil
}

// fakeCollectorAgent serves canned tail text keyed by "addr/name".
type fakeCollectorAgent struct {
	logs map[string]string
}

func (a *fakeCollectorAgent) TailLogs(_ context.Context, addr, name string, _ int) (string, error) {
	return a.logs[addr+"/"+name], nil
}

// fakeCollectorDriver resolves node addresses as "<node>-addr".
type fakeCollectorDriver struct {
	runtime.Driver
}

func (fakeCollectorDriver) NodeAddress(_ context.Context, _, nodeName string) (string, error) {
	return nodeName + "-addr", nil
}

type fakeStopper struct {
	stopped []string
}

func (s *fakeStopper) StopTrafficScenario(_ context.Context, _, id string) (*model.TrafficScenario, error) {
	s.stopped = append(s.stopped, id)

	return &model.TrafficScenario{Meta: model.ResourceMeta{ID: id}, Status: model.TrafficScenarioStatus{Phase: model.TrafficPhaseStopped}}, nil
}

func runningScenario() *model.TrafficScenario {
	return &model.TrafficScenario{
		Meta: model.ResourceMeta{ID: "t-1", Name: "t-1"},
		Spec: model.TrafficScenarioSpec{LabID: "lab-1", SourceServerName: "server-1"},
		Status: model.TrafficScenarioStatus{
			Phase: model.TrafficPhaseRunning, ClientProgramName: "t-1-client", StartedAt: time.Now().UTC(),
		},
	}
}

func deployedLab() *model.Lab {
	return &model.Lab{Meta: model.ResourceMeta{ID: "lab-1", Name: "dc1", Generation: 1}}
}

func TestCollectorSweep_FirstSampleSeedsOnly(t *testing.T) {
	sc := runningScenario()
	store := &fakeStore{labs: []*model.Lab{deployedLab()}, scenarios: map[string][]*model.TrafficScenario{"lab-1": {sc}}}
	agent := &fakeCollectorAgent{logs: map[string]string{
		"server-1-addr/t-1-client": `{"msg":"stats","total":5,"failed":0,"rate":1,"lat_p50_us":100,"lat_p95_us":150,"lat_p99_us":200}` + "\n",
	}}
	history := NewHistory()
	c := NewCollector(store, agent, fakeCollectorDriver{}, &fakeStopper{}, history, testLogger())

	c.sweep(context.Background())

	if _, ok := history.Last("t-1"); ok {
		t.Error("first sample should only seed the baseline, not produce a point")
	}

	if len(store.updates) != 0 {
		t.Errorf("no status update expected on first sample, got %v", store.updates)
	}
}

func TestCollectorSweep_SecondSampleProducesPoint(t *testing.T) {
	sc := runningScenario()
	sc.Spec.Assertions = []model.TrafficAssertion{
		{Metric: model.TrafficMetricSuccessRate, Comparator: model.TrafficComparatorGTE, Threshold: 99},
	}

	store := &fakeStore{labs: []*model.Lab{deployedLab()}, scenarios: map[string][]*model.TrafficScenario{"lab-1": {sc}}}
	agent := &fakeCollectorAgent{logs: map[string]string{
		"server-1-addr/t-1-client": `{"msg":"stats","total":5,"failed":0,"rate":1,"lat_p50_us":100,"lat_p95_us":150,"lat_p99_us":200}` + "\n",
	}}
	history := NewHistory()
	c := NewCollector(store, agent, fakeCollectorDriver{}, &fakeStopper{}, history, testLogger())

	c.sweep(context.Background()) // seeds baseline (total=5)

	agent.logs["server-1-addr/t-1-client"] = `{"msg":"stats","total":15,"failed":1,"rate":2,"lat_p50_us":120,"lat_p95_us":180,"lat_p99_us":250}` + "\n"
	c.sweep(context.Background()) // total delta=10, failed delta=1 -> successRate=90%

	point, ok := history.Last("t-1")
	if !ok {
		t.Fatal("expected a point after the second sweep")
	}

	if point.Rate != 2 || point.SuccessRate != 90 || point.P50Us != 120 {
		t.Errorf("point = %+v", point)
	}

	if len(store.updates) != 1 || store.updates[0] != "t-1" {
		t.Errorf("updates = %v, want [t-1]", store.updates)
	}

	if len(sc.Status.Assertions) != 1 || sc.Status.Assertions[0].Pass {
		t.Errorf("assertions = %+v, want successRate>=99 to fail at 90%%", sc.Status.Assertions)
	}
}

func TestCollectorSweep_NoNewDataSkipsUpdate(t *testing.T) {
	sc := runningScenario()
	store := &fakeStore{labs: []*model.Lab{deployedLab()}, scenarios: map[string][]*model.TrafficScenario{"lab-1": {sc}}}
	agent := &fakeCollectorAgent{logs: map[string]string{
		"server-1-addr/t-1-client": `{"msg":"stats","total":5,"failed":0,"rate":1}` + "\n",
	}}
	history := NewHistory()
	c := NewCollector(store, agent, fakeCollectorDriver{}, &fakeStopper{}, history, testLogger())

	c.sweep(context.Background()) // seeds baseline
	c.sweep(context.Background()) // same total: no new data

	if _, ok := history.Last("t-1"); ok {
		t.Error("unchanged total should not produce a new point")
	}

	if len(store.updates) != 0 {
		t.Errorf("updates = %v, want none", store.updates)
	}
}

func TestCollectorSweep_ExpiredScenarioAutoStops(t *testing.T) {
	sc := runningScenario()
	sc.Spec.Duration = time.Minute
	sc.Status.StartedAt = time.Now().UTC().Add(-2 * time.Minute)
	store := &fakeStore{labs: []*model.Lab{deployedLab()}, scenarios: map[string][]*model.TrafficScenario{"lab-1": {sc}}}
	stopper := &fakeStopper{}
	c := NewCollector(store, &fakeCollectorAgent{logs: map[string]string{}}, fakeCollectorDriver{}, stopper, NewHistory(), testLogger())

	c.sweep(context.Background())

	if len(stopper.stopped) != 1 || stopper.stopped[0] != "t-1" {
		t.Errorf("stopped = %v, want [t-1]", stopper.stopped)
	}
}

func TestCollectorSweep_RetainsOnlyActiveScenarios(t *testing.T) {
	history := NewHistory()
	history.AppendPoint("gone", model.TrafficPoint{Ts: time.Now()})

	store := &fakeStore{labs: []*model.Lab{deployedLab()}}
	c := NewCollector(store, &fakeCollectorAgent{logs: map[string]string{}}, fakeCollectorDriver{}, &fakeStopper{}, history, testLogger())

	c.sweep(context.Background())

	if _, ok := history.Last("gone"); ok {
		t.Error("a scenario no longer listed by the store should be retained out of history")
	}
}
