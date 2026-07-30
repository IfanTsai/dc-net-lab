package metrics

import (
	"context"
	"fmt"
	"io"
	"log/slog"
	"sync"
	"testing"
	"time"

	"github.com/ifantsai/dcnetlab/controller/internal/conf"
	"github.com/ifantsai/dcnetlab/internal/model"
	"github.com/ifantsai/dcnetlab/internal/runtime"
)

// fakeStore serves one deployed lab with the given nodes.
type fakeStore struct {
	labs  []*model.Lab
	nodes []*model.Node
}

func (s *fakeStore) ListLabs() ([]*model.Lab, error) { return s.labs, nil }

func (s *fakeStore) ListNodes(string) ([]*model.Node, error) { return s.nodes, nil }

// fakeAgent returns a canned scrape per address.
type fakeAgent struct {
	mu      sync.Mutex
	samples map[string]*model.NodeMetrics
	fail    map[string]bool
}

func (a *fakeAgent) Metrics(_ context.Context, addr string) (*model.NodeMetrics, error) {
	a.mu.Lock()
	defer a.mu.Unlock()

	if a.fail[addr] {
		return nil, fmt.Errorf("agent unreachable")
	}

	m, ok := a.samples[addr]
	if !ok {
		return nil, fmt.Errorf("no sample for %s", addr)
	}

	clone := *m

	return &clone, nil
}

// fakeDriver resolves node addresses as "<node>-addr".
type fakeDriver struct {
	runtime.Driver
}

func (fakeDriver) NodeAddress(_ context.Context, _, nodeName string) (string, error) {
	return nodeName + "-addr", nil
}

func testCollector(t *testing.T) (*Collector, *fakeAgent, *History) {
	t.Helper()

	lab := &model.Lab{Meta: model.ResourceMeta{
		ID: "lab-1", Name: "dc1", Generation: 1, Phase: model.PhaseRunning,
	}}
	nodes := []*model.Node{
		{
			Meta: model.ResourceMeta{ID: "n-1", Name: "server-1", ObservedGeneration: 1},
			Spec: model.NodeSpec{Role: model.RoleServer},
		},
		{
			Meta: model.ResourceMeta{ID: "n-2", Name: "leaf-1", ObservedGeneration: 1},
			Spec: model.NodeSpec{Role: model.RoleLeaf},
		},
	}

	log := slog.New(slog.NewTextHandler(io.Discard, nil))
	history := NewHistory(&conf.Data{Dir: t.TempDir()}, log)
	t.Cleanup(history.Close)

	agent := &fakeAgent{samples: make(map[string]*model.NodeMetrics), fail: make(map[string]bool)}
	c := NewCollector(&fakeStore{labs: []*model.Lab{lab}, nodes: nodes}, agent, fakeDriver{}, history, log)

	return c, agent, history
}

// sampleWith builds a counters-only agent reply.
func sampleWith(cpuSec float64, rxBytes int64) *model.NodeMetrics {
	return &model.NodeMetrics{
		Procs: 3,
		CPU:   model.MetricsCPU{LimitCores: 2, UsageSecondsTotal: cpuSec, UserSecondsTotal: cpuSec},
		Interfaces: []model.MetricsInterface{
			{Name: "eth1", RxBytesTotal: rxBytes},
		},
		Disk: model.MetricsDisk{ReadBytesTotal: rxBytes},
	}
}

func TestCollectorDiffsAcrossSweeps(t *testing.T) {
	c, agent, history := testCollector(t)

	base := time.Now().UTC().Add(-time.Minute)
	c.now = func() time.Time { return base }
	agent.samples["server-1-addr"] = sampleWith(100, 1000)
	c.sweep(context.Background())

	// First sweep only seeds the baseline: no visible point.
	if points := history.Query("lab-1", "server-1", base.Add(-time.Hour), base.Add(time.Hour)); len(points) != 0 {
		t.Fatalf("first sweep produced points: %+v", points)
	}

	// 15 s later: +3 CPU seconds on 2 cores = 10 %, +1500 B rx = 100 B/s.
	c.now = func() time.Time { return base.Add(15 * time.Second) }
	agent.samples["server-1-addr"] = sampleWith(103, 2500)
	c.sweep(context.Background())

	points := history.Query("lab-1", "server-1", base.Add(-time.Hour), base.Add(time.Hour))
	if len(points) != 1 {
		t.Fatalf("got %d points, want 1", len(points))
	}

	p := points[0]
	if p.CPU.UsagePercent != 10 {
		t.Errorf("cpu = %+v, want 10%%", p.CPU)
	}

	if p.Interfaces[0].RxBytesPerSec != 100 || p.Disk.ReadBytesPerSec != 100 {
		t.Errorf("rates = iface %+v disk %+v", p.Interfaces[0], p.Disk)
	}

	if p.Procs != 3 || p.CPU.LimitCores != 2 {
		t.Errorf("gauges lost: %+v", p)
	}
}

func TestCollectorGapsAndResets(t *testing.T) {
	c, agent, history := testCollector(t)
	base := time.Now().UTC().Add(-time.Minute)

	c.now = func() time.Time { return base }
	agent.samples["server-1-addr"] = sampleWith(100, 1000)
	c.sweep(context.Background())

	// Unreachable agent: gap, but the baseline survives.
	c.now = func() time.Time { return base.Add(15 * time.Second) }
	agent.fail["server-1-addr"] = true
	c.sweep(context.Background())

	// Counter reset (container restart): rates clamp to zero.
	c.now = func() time.Time { return base.Add(30 * time.Second) }
	agent.fail["server-1-addr"] = false
	agent.samples["server-1-addr"] = sampleWith(1, 10)
	c.sweep(context.Background())

	points := history.Query("lab-1", "server-1", base.Add(-time.Hour), base.Add(time.Hour))
	if len(points) != 1 {
		t.Fatalf("got %d points, want 1 (gap for the failed sweep)", len(points))
	}

	if points[0].CPU.UsagePercent != 0 || points[0].Interfaces[0].RxBytesPerSec != 0 {
		t.Errorf("reset rates not clamped: %+v", points[0])
	}
}

func TestCollectorStaleBaseline(t *testing.T) {
	c, agent, history := testCollector(t)
	base := time.Now().UTC().Add(-2 * BaselineMaxAge)

	c.now = func() time.Time { return base }
	agent.samples["server-1-addr"] = sampleWith(100, 1000)
	c.sweep(context.Background())

	// Next sample far beyond baselineMaxAge: dropped as a gap, the
	// one after resumes normally.
	now := time.Now().UTC()
	c.now = func() time.Time { return now }
	agent.samples["server-1-addr"] = sampleWith(5000, 9000)
	c.sweep(context.Background())

	c.now = func() time.Time { return now.Add(15 * time.Second) }
	agent.samples["server-1-addr"] = sampleWith(5003, 9600)
	c.sweep(context.Background())

	points := history.Query("lab-1", "server-1", now.Add(-time.Minute), now.Add(time.Minute))
	if len(points) != 1 {
		t.Fatalf("got %d points, want 1", len(points))
	}

	if points[0].CPU.UsagePercent != 10 || points[0].Interfaces[0].RxBytesPerSec != 40 {
		t.Errorf("resumed point = %+v", points[0])
	}
}
