package biz

import (
	"context"
	"fmt"
	"io"
	"log/slog"
	"strings"
	"testing"
	"time"

	"github.com/ifantsai/dcnetlab/internal/conf"
	"github.com/ifantsai/dcnetlab/internal/model"
	"github.com/ifantsai/dcnetlab/internal/runtime"
)

// fakeProgramRepo serves the lab and nodes and keeps created
// programs in memory.
type fakeProgramRepo struct {
	ProgramRepo

	lab      *model.Lab
	nodes    []*model.Node
	programs []*model.Program
}

func (r *fakeProgramRepo) GetLab(id string) (*model.Lab, error) {
	if r.lab == nil || r.lab.Meta.ID != id {
		return nil, ErrNotFound
	}

	return r.lab, nil
}

func (r *fakeProgramRepo) ListNodes(string) ([]*model.Node, error) { return r.nodes, nil }

func (r *fakeProgramRepo) CreateProgram(p *model.Program) error {
	r.programs = append(r.programs, p)

	return nil
}

func (r *fakeProgramRepo) UpdateProgram(*model.Program) error { return nil }

func (r *fakeProgramRepo) ListPrograms(string) ([]*model.Program, error) { return r.programs, nil }

// fakeAgent records package installs and fails for listed servers
// (keyed by agent address, which the fake driver derives from the
// node name).
type fakeAgent struct {
	ProgramAgent

	installs []string // "addr name@version"
	failFor  map[string]bool
}

func (a *fakeAgent) InstallPackage(_ context.Context, addr string, pkg AgentPackage) error {
	if a.failFor[addr] {
		return fmt.Errorf("agent unreachable")
	}

	a.installs = append(a.installs, fmt.Sprintf("%s %s@%s", addr, pkg.Name, pkg.Version))

	return nil
}

func (a *fakeAgent) Install(_ context.Context, _ string, _ *model.Program) error { return nil }

func (a *fakeAgent) Metrics(_ context.Context, addr string) (*model.NodeMetrics, error) {
	if a.failFor[addr] {
		return nil, fmt.Errorf("agent unreachable")
	}

	return &model.NodeMetrics{
		SampledAt: time.Now().UTC(),
		Procs:     3,
		CPU:       model.MetricsCPU{LimitCores: 2, UsageSecondsTotal: 100},
	}, nil
}

// fakeHistory serves one canned series regardless of range and an
// optional diff baseline.
type fakeHistory struct {
	points   []model.MetricsPoint
	baseline *model.MetricsPoint
	calls    []string // "labID/server"
}

func (h *fakeHistory) Query(labID, server string, _, _ time.Time) []model.MetricsPoint {
	h.calls = append(h.calls, labID+"/"+server)

	return h.points
}

func (h *fakeHistory) Last(string, string) (model.MetricsPoint, bool) {
	if h.baseline == nil {
		return model.MetricsPoint{}, false
	}

	return *h.baseline, true
}

// fakeDriver resolves node addresses as "<node>-addr".
type fakeDriver struct {
	runtime.Driver
}

func (fakeDriver) NodeAddress(_ context.Context, _, nodeName string) (string, error) {
	return nodeName + "-addr", nil
}

func (fakeDriver) NodeGateway(_ context.Context, _, _ string) (string, error) {
	return "172.20.20.1", nil
}

func serverNodeNamed(id, name string) *model.Node {
	return &model.Node{
		Meta: model.ResourceMeta{ID: id, Name: name},
		Spec: model.NodeSpec{Role: model.RoleServer},
	}
}

// testProgramUsecase builds the usecase over fakes with one uploaded
// package web@1.0.0 and the given lab nodes.
func testProgramUsecase(t *testing.T, lab *model.Lab, nodes []*model.Node) (*ProgramUsecase, *fakeAgent) {
	t.Helper()

	log := slog.New(slog.NewTextHandler(io.Discard, nil))
	packages, err := NewPackageUsecase(newFakePackageRepo(), &conf.Data{}, log)
	if err != nil {
		t.Fatal(err)
	}

	payload := archiveOf(t, PackageManifest{Name: "web", Version: "1.0.0", Entrypoint: "run.sh"})
	if _, err := packages.UploadPackage(payload); err != nil {
		t.Fatal(err)
	}

	agent := &fakeAgent{failFor: make(map[string]bool)}
	repo := &fakeProgramRepo{lab: lab, nodes: nodes}
	sc := &conf.Server{RepoAddr: "0.0.0.0:50062"}
	uc := NewProgramUsecase(repo, agent, fakeDriver{}, packages, &fakeHistory{}, sc, log)

	return uc, agent
}

func deployedLab() *model.Lab {
	return &model.Lab{Meta: model.ResourceMeta{
		ID: "lab-1", Name: "dc1", Generation: 1, CreatedAt: time.Now(), UpdatedAt: time.Now(),
	}}
}

func TestInstallPackageOnServers(t *testing.T) {
	nodes := []*model.Node{
		serverNodeNamed("n-1", "server-1"),
		{Meta: model.ResourceMeta{ID: "n-2", Name: "leaf-1"}, Spec: model.NodeSpec{Role: model.RoleLeaf}},
		serverNodeNamed("n-3", "server-2"),
	}

	t.Run("all servers", func(t *testing.T) {
		uc, agent := testProgramUsecase(t, deployedLab(), nodes)

		results, err := uc.InstallPackageOnServers(context.Background(), "lab-1", "web", "1.0.0", nil)
		if err != nil {
			t.Fatal(err)
		}

		if len(results) != 2 {
			t.Fatalf("results = %+v, want 2 servers", results)
		}

		for _, r := range results {
			if r.Err != nil {
				t.Errorf("server %s: %v", r.ServerName, r.Err)
			}
		}

		if len(agent.installs) != 2 || !strings.Contains(agent.installs[0], "web@1.0.0") {
			t.Errorf("installs = %v", agent.installs)
		}
	})

	t.Run("per-server failure is reported, not fatal", func(t *testing.T) {
		uc, agent := testProgramUsecase(t, deployedLab(), nodes)
		agent.failFor["server-1-addr"] = true

		results, err := uc.InstallPackageOnServers(context.Background(), "lab-1", "web", "1.0.0", nil)
		if err != nil {
			t.Fatal(err)
		}

		if results[0].Err == nil || results[1].Err != nil {
			t.Fatalf("results = %+v, want first failed and second ok", results)
		}
	})

	t.Run("explicit server selection", func(t *testing.T) {
		uc, agent := testProgramUsecase(t, deployedLab(), nodes)

		results, err := uc.InstallPackageOnServers(context.Background(), "lab-1", "web", "1.0.0", []string{"n-3"})
		if err != nil {
			t.Fatal(err)
		}

		if len(results) != 1 || results[0].ServerName != "server-2" || len(agent.installs) != 1 {
			t.Fatalf("results = %+v installs = %v", results, agent.installs)
		}
	})

	t.Run("batch create programs", func(t *testing.T) {
		uc, _ := testProgramUsecase(t, deployedLab(), nodes)
		spec := model.ProgramSpec{PackageName: "web", PackageVersion: "1.0.0", RestartPolicy: "Always"}

		programs, failures, err := uc.CreateProgram("lab-1", "svc", spec, []string{"n-1", "n-3"})
		if err != nil {
			t.Fatal(err)
		}

		if len(programs) != 2 || len(failures) != 0 {
			t.Fatalf("programs = %d failures = %+v, want 2/0", len(programs), failures)
		}

		if programs[0].Spec.ServerName != "server-1" || programs[1].Spec.ServerName != "server-2" {
			t.Errorf("server binding: %s, %s", programs[0].Spec.ServerName, programs[1].Spec.ServerName)
		}

		// A duplicate as the only target aborts the whole call.
		programs, _, err = uc.CreateProgram("lab-1", "svc", spec, []string{"n-1"})
		if err == nil || len(programs) != 0 {
			t.Fatalf("duplicate on a single server should fail: programs=%d err=%v", len(programs), err)
		}

		_, failures, err = uc.CreateProgram("lab-1", "svc2", spec, []string{"n-1", "nosuch"})
		if err != nil {
			t.Fatal(err)
		}

		if len(failures) != 1 || failures[0].ServerID != "nosuch" {
			t.Errorf("failures = %+v, want the unknown server", failures)
		}
	})

	t.Run("oneshot with Always rejected", func(t *testing.T) {
		uc, _ := testProgramUsecase(t, deployedLab(), nodes)
		spec := model.ProgramSpec{PackageName: "web", PackageVersion: "1.0.0",
			Type: "oneshot", RestartPolicy: "Always"}

		if _, _, err := uc.CreateProgram("lab-1", "task", spec, []string{"n-1"}); err == nil {
			t.Fatal("expected error")
		}
	})

	tests := []struct {
		name      string
		labID     string
		pkg       string
		version   string
		serverIDs []string
		lab       *model.Lab
	}{
		{name: "unknown package", labID: "lab-1", pkg: "nosuch", version: "1.0.0", lab: deployedLab()},
		{name: "non-server target", labID: "lab-1", pkg: "web", version: "1.0.0", serverIDs: []string{"n-2"}, lab: deployedLab()},
		{name: "undeployed lab", labID: "lab-1", pkg: "web", version: "1.0.0",
			lab: &model.Lab{Meta: model.ResourceMeta{ID: "lab-1", Name: "dc1"}}},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			uc, _ := testProgramUsecase(t, tt.lab, nodes)

			if _, err := uc.InstallPackageOnServers(context.Background(), tt.labID, tt.pkg, tt.version, tt.serverIDs); err == nil {
				t.Fatal("expected error")
			}
		})
	}
}

func TestNodeMetrics(t *testing.T) {
	nodes := []*model.Node{
		serverNodeNamed("n-1", "server-1"),
		{Meta: model.ResourceMeta{ID: "n-2", Name: "leaf-1"}, Spec: model.NodeSpec{Role: model.RoleLeaf}},
	}

	t.Run("no baseline: gauges only, zero rates", func(t *testing.T) {
		uc, _ := testProgramUsecase(t, deployedLab(), nodes)

		m, err := uc.NodeMetrics(context.Background(), "lab-1", "n-1")
		if err != nil {
			t.Fatal(err)
		}

		if m.Procs != 3 || m.CPU.UsagePercent != 0 {
			t.Errorf("metrics = %+v", m)
		}
	})

	t.Run("rates diffed against collector baseline", func(t *testing.T) {
		uc, _ := testProgramUsecase(t, deployedLab(), nodes)
		// Baseline 10 s ago with 99 CPU seconds; the fake scrape
		// reports 100 on 2 cores: (100-99)/10s/2 = 5 %.
		uc.history = &fakeHistory{baseline: &model.MetricsPoint{
			Ts:  time.Now().UTC().Add(-10 * time.Second),
			CPU: model.MetricsCPU{UsageSecondsTotal: 99},
		}}

		m, err := uc.NodeMetrics(context.Background(), "lab-1", "n-1")
		if err != nil {
			t.Fatal(err)
		}

		if m.CPU.UsagePercent < 4.9 || m.CPU.UsagePercent > 5.1 {
			t.Errorf("usage = %v, want ~5%%", m.CPU.UsagePercent)
		}
	})

	tests := []struct {
		name   string
		nodeID string
		lab    *model.Lab
	}{
		{name: "non-server node", nodeID: "n-2", lab: deployedLab()},
		{name: "unknown node", nodeID: "n-9", lab: deployedLab()},
		{name: "undeployed lab", nodeID: "n-1",
			lab: &model.Lab{Meta: model.ResourceMeta{ID: "lab-1", Name: "dc1"}}},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			uc, _ := testProgramUsecase(t, tt.lab, nodes)

			if _, err := uc.NodeMetrics(context.Background(), "lab-1", tt.nodeID); err == nil {
				t.Fatal("expected error")
			}
		})
	}

	t.Run("agent unreachable", func(t *testing.T) {
		uc, agent := testProgramUsecase(t, deployedLab(), nodes)
		agent.failFor["server-1-addr"] = true

		if _, err := uc.NodeMetrics(context.Background(), "lab-1", "n-1"); err == nil {
			t.Fatal("expected error")
		}
	})
}

func TestNodeMetricsHistory(t *testing.T) {
	nodes := []*model.Node{
		serverNodeNamed("n-1", "server-1"),
		{Meta: model.ResourceMeta{ID: "n-2", Name: "leaf-1"}, Spec: model.NodeSpec{Role: model.RoleLeaf}},
	}

	t.Run("server", func(t *testing.T) {
		uc, _ := testProgramUsecase(t, deployedLab(), nodes)
		history := &fakeHistory{points: []model.MetricsPoint{{Procs: 3}}}
		uc.history = history

		points, err := uc.NodeMetricsHistory("lab-1", "n-1", time.Time{}, time.Time{})
		if err != nil {
			t.Fatal(err)
		}

		if len(points) != 1 || points[0].Procs != 3 {
			t.Errorf("points = %+v", points)
		}

		if len(history.calls) != 1 || history.calls[0] != "lab-1/server-1" {
			t.Errorf("queried %v, want lab-1/server-1", history.calls)
		}
	})

	t.Run("non-server node", func(t *testing.T) {
		uc, _ := testProgramUsecase(t, deployedLab(), nodes)

		if _, err := uc.NodeMetricsHistory("lab-1", "n-2", time.Time{}, time.Time{}); err == nil {
			t.Fatal("expected error")
		}
	})

	t.Run("inverted range", func(t *testing.T) {
		uc, _ := testProgramUsecase(t, deployedLab(), nodes)
		now := time.Now()

		if _, err := uc.NodeMetricsHistory("lab-1", "n-1", now, now.Add(-time.Hour)); err == nil {
			t.Fatal("expected error")
		}
	})
}
