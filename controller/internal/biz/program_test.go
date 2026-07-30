package biz

import (
	"context"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"strings"
	"testing"
	"time"

	"github.com/ifantsai/dcnetlab/controller/internal/conf"
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

func (r *fakeProgramRepo) GetProgram(id string) (*model.Program, error) {
	for _, p := range r.programs {
		if p.Meta.ID == id {
			return p, nil
		}
	}

	return nil, ErrNotFound
}

func (r *fakeProgramRepo) DeleteProgram(id string) error {
	for i, p := range r.programs {
		if p.Meta.ID == id {
			r.programs = append(r.programs[:i], r.programs[i+1:]...)

			return nil
		}
	}

	return ErrNotFound
}

// fakeAgent records package installs and fails for listed servers
// (keyed by agent address, which the fake driver derives from the
// node name).
type fakeAgent struct {
	ProgramAgent

	installs   []string         // "addr name@version"
	registered []*model.Program // programs passed to Install, in call order
	started    []string         // program names passed to Start
	stopped    []string         // program names passed to Stop
	removed    []string         // program names passed to Remove
	failFor    map[string]bool
}

func (a *fakeAgent) InstallPackage(_ context.Context, addr string, pkg AgentPackage) error {
	if a.failFor[addr] {
		return fmt.Errorf("agent unreachable")
	}

	a.installs = append(a.installs, fmt.Sprintf("%s %s@%s", addr, pkg.Name, pkg.Version))

	return nil
}

func (a *fakeAgent) Install(_ context.Context, _ string, p *model.Program) error {
	a.registered = append(a.registered, p)

	return nil
}

func (a *fakeAgent) Start(_ context.Context, _, name string) (model.ProgramStatus, error) {
	a.started = append(a.started, name)

	return model.ProgramStatus{State: model.ProgramStateRunning, PID: 1}, nil
}

func (a *fakeAgent) Stop(_ context.Context, _, name string) (model.ProgramStatus, error) {
	a.stopped = append(a.stopped, name)

	return model.ProgramStatus{State: model.ProgramStateStopped}, nil
}

func (a *fakeAgent) Remove(_ context.Context, _, name string) error {
	a.removed = append(a.removed, name)

	return nil
}

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

		programs, failures, err := uc.CreateProgram("lab-1", "svc", spec, []string{"n-1", "n-3"}, false)
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
		programs, _, err = uc.CreateProgram("lab-1", "svc", spec, []string{"n-1"}, false)
		if err == nil || len(programs) != 0 {
			t.Fatalf("duplicate on a single server should fail: programs=%d err=%v", len(programs), err)
		}

		_, failures, err = uc.CreateProgram("lab-1", "svc2", spec, []string{"n-1", "nosuch"}, false)
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

		if _, _, err := uc.CreateProgram("lab-1", "task", spec, []string{"n-1"}, false); err == nil {
			t.Fatal("expected error")
		}
	})

	t.Run("liveness check validated", func(t *testing.T) {
		uc, _ := testProgramUsecase(t, deployedLab(), nodes)

		bad := model.ProgramSpec{PackageName: "web", PackageVersion: "1.0.0",
			LivenessCheck: &model.HealthCheck{Type: "tcp"}} // tcp without a port
		if _, _, err := uc.CreateProgram("lab-1", "probed", bad, []string{"n-1"}, false); err == nil {
			t.Fatal("tcp liveness check without a port should be rejected")
		}

		good := model.ProgramSpec{PackageName: "web", PackageVersion: "1.0.0",
			LivenessCheck: &model.HealthCheck{Type: "http", Port: 8080, Path: "/healthz"}}
		programs, _, err := uc.CreateProgram("lab-1", "probed", good, []string{"n-1"}, false)
		if err != nil {
			t.Fatalf("valid liveness check rejected: %v", err)
		}

		if len(programs) != 1 || programs[0].Spec.LivenessCheck == nil {
			t.Fatalf("liveness check not persisted on the program: %+v", programs)
		}
	})

	t.Run("create with start launches immediately", func(t *testing.T) {
		uc, agent := testProgramUsecase(t, deployedLab(), nodes)
		spec := model.ProgramSpec{PackageName: "web", PackageVersion: "1.0.0", RestartPolicy: "Always"}

		programs, _, err := uc.CreateProgram("lab-1", "svc", spec, []string{"n-1", "n-3"}, true)
		if err != nil {
			t.Fatal(err)
		}

		if len(programs) != 2 {
			t.Fatalf("programs = %d, want 2", len(programs))
		}

		for _, p := range programs {
			if p.Spec.DesiredState != model.ProgramDesiredRunning || p.Status.State != model.ProgramStateRunning {
				t.Errorf("program %s not started: desired=%s state=%s",
					p.Meta.Name, p.Spec.DesiredState, p.Status.State)
			}
		}

		if len(agent.started) != 2 {
			t.Errorf("agent.Start calls = %d, want 2", len(agent.started))
		}
	})

	t.Run("create oneshot with start keeps desire stopped", func(t *testing.T) {
		uc, agent := testProgramUsecase(t, deployedLab(), nodes)
		spec := model.ProgramSpec{PackageName: "web", PackageVersion: "1.0.0",
			Type: "oneshot", RestartPolicy: "Never"}

		programs, _, err := uc.CreateProgram("lab-1", "task", spec, []string{"n-1"}, true)
		if err != nil {
			t.Fatal(err)
		}

		// A oneshot runs once now, but its standing desire stays stopped
		// so a redeploy does not re-run it (only auto-start would).
		if programs[0].Spec.DesiredState != model.ProgramDesiredStopped {
			t.Errorf("oneshot desired = %s, want Stopped", programs[0].Spec.DesiredState)
		}

		if len(agent.started) != 1 {
			t.Errorf("oneshot should still run once: Start calls = %d", len(agent.started))
		}
	})

	t.Run("batch start, stop and delete", func(t *testing.T) {
		uc, agent := testProgramUsecase(t, deployedLab(), nodes)
		spec := model.ProgramSpec{PackageName: "web", PackageVersion: "1.0.0", RestartPolicy: "Always"}

		programs, _, err := uc.CreateProgram("lab-1", "svc", spec, []string{"n-1", "n-3"}, false)
		if err != nil {
			t.Fatal(err)
		}

		ids := []string{programs[0].Meta.ID, programs[1].Meta.ID}

		// An invalid op is rejected outright.
		if _, err := uc.BatchProgramOp(context.Background(), "lab-1", "restart", ids); err == nil {
			t.Fatal("invalid op should be rejected")
		}

		// Start both plus one unknown id: the unknown fails, the rest go
		// through (best-effort, not aborted).
		outcomes, err := uc.BatchProgramOp(context.Background(), "lab-1", BatchOpStart, append(ids, "nosuch"))
		if err != nil {
			t.Fatal(err)
		}

		if len(outcomes) != 3 || outcomes[0].Err != nil || outcomes[1].Err != nil || outcomes[2].Err == nil {
			t.Fatalf("outcomes = %+v, want first two ok and the last failed", outcomes)
		}

		if len(agent.started) != 2 {
			t.Errorf("agent.Start calls = %d, want 2", len(agent.started))
		}

		if _, err := uc.BatchProgramOp(context.Background(), "lab-1", BatchOpStop, ids); err != nil {
			t.Fatal(err)
		}

		if len(agent.stopped) != 2 {
			t.Errorf("agent.Stop calls = %d, want 2", len(agent.stopped))
		}

		if _, err := uc.BatchProgramOp(context.Background(), "lab-1", BatchOpDelete, ids); err != nil {
			t.Fatal(err)
		}

		if len(agent.removed) != 2 {
			t.Errorf("agent.Remove calls = %d, want 2", len(agent.removed))
		}

		if remaining, _ := uc.repo.ListPrograms("lab-1"); len(remaining) != 0 {
			t.Errorf("programs after batch delete = %d, want 0", len(remaining))
		}
	})

	t.Run("noop runtime explains the fix", func(t *testing.T) {
		uc, _ := testProgramUsecase(t, deployedLab(), nodes)
		spec := model.ProgramSpec{PackageName: "web", PackageVersion: "1.0.0", RestartPolicy: "Always"}

		programs, _, err := uc.CreateProgram("lab-1", "svc", spec, []string{"n-1"}, false)
		if err != nil {
			t.Fatal(err)
		}

		// Swap in the noop driver: agent operations must fail with
		// guidance (orb-setup on macOS), not the bare driver sentinel.
		uc.driver = runtime.NoopDriver{}

		_, err = uc.StartProgram(context.Background(), "lab-1", programs[0].Meta.ID)
		if !errors.Is(err, runtime.ErrNotSupported) {
			t.Fatalf("err = %v, want to wrap ErrNotSupported", err)
		}

		if !strings.Contains(err.Error(), "orb-setup") {
			t.Errorf("err = %v, want actionable orb-setup hint", err)
		}
	})

	t.Run("readiness check and startup order", func(t *testing.T) {
		uc, _ := testProgramUsecase(t, deployedLab(), nodes)

		bad := model.ProgramSpec{PackageName: "web", PackageVersion: "1.0.0",
			ReadinessCheck: &model.HealthCheck{Type: "http"}} // http without a port
		if _, _, err := uc.CreateProgram("lab-1", "gated", bad, []string{"n-1"}, false); err == nil {
			t.Fatal("http readiness check without a port should be rejected")
		}

		good := model.ProgramSpec{PackageName: "web", PackageVersion: "1.0.0",
			ReadinessCheck: &model.HealthCheck{Type: "tcp", Port: 9090}, StartupOrder: 5}
		programs, _, err := uc.CreateProgram("lab-1", "gated", good, []string{"n-1"}, false)
		if err != nil {
			t.Fatalf("valid readiness check rejected: %v", err)
		}

		if len(programs) != 1 || programs[0].Spec.ReadinessCheck == nil || programs[0].Spec.StartupOrder != 5 {
			t.Fatalf("readiness/order not persisted: %+v", programs)
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
