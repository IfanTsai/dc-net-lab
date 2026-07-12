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
	uc := NewProgramUsecase(repo, agent, fakeDriver{}, packages, sc, log)

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
