package biz

import (
	"context"
	"io"
	"log/slog"
	"net/netip"
	"testing"
	"time"

	"github.com/ifantsai/dcnetlab/internal/conf"
	"github.com/ifantsai/dcnetlab/internal/model"
)

// fakeTrafficRepo serves the lab and nodes and keeps created scenarios
// in memory.
type fakeTrafficRepo struct {
	TrafficRepo

	lab       *model.Lab
	nodes     []*model.Node
	scenarios []*model.TrafficScenario
}

func (r *fakeTrafficRepo) GetLab(id string) (*model.Lab, error) {
	if r.lab == nil || r.lab.Meta.ID != id {
		return nil, ErrNotFound
	}

	return r.lab, nil
}

func (r *fakeTrafficRepo) ListNodes(string) ([]*model.Node, error) { return r.nodes, nil }

func (r *fakeTrafficRepo) CreateTrafficScenario(s *model.TrafficScenario) error {
	r.scenarios = append(r.scenarios, s)

	return nil
}

func (r *fakeTrafficRepo) UpdateTrafficScenario(*model.TrafficScenario) error { return nil }

func (r *fakeTrafficRepo) DeleteTrafficScenario(id string) error {
	for i, s := range r.scenarios {
		if s.Meta.ID == id {
			r.scenarios = append(r.scenarios[:i], r.scenarios[i+1:]...)

			return nil
		}
	}

	return ErrNotFound
}

func (r *fakeTrafficRepo) GetTrafficScenario(id string) (*model.TrafficScenario, error) {
	for _, s := range r.scenarios {
		if s.Meta.ID == id {
			return s, nil
		}
	}

	return nil, ErrNotFound
}

func (r *fakeTrafficRepo) ListTrafficScenarios(string) ([]*model.TrafficScenario, error) {
	return r.scenarios, nil
}

// fakeTrafficHistory serves one canned series regardless of range.
type fakeTrafficHistory struct {
	points []model.TrafficPoint
}

func (h *fakeTrafficHistory) Query(string, time.Time, time.Time) []model.TrafficPoint {
	return h.points
}

func (h *fakeTrafficHistory) Last(string) (model.TrafficPoint, bool) {
	return model.TrafficPoint{}, false
}

func serverNodeWithAddr(id, name, cidr string) *model.Node {
	n := serverNodeNamed(id, name)
	n.Spec.Address = netip.MustParsePrefix(cidr)

	return n
}

// testTrafficUsecase builds the traffic usecase over a real
// ProgramUsecase (fakes underneath) with the builtin trafficgen
// package seeded directly into the fake package repo (UploadPackage
// rejects the reserved builtin name, so tests bypass it the way the
// controller's own registerBuiltin would).
func testTrafficUsecase(t *testing.T, lab *model.Lab, nodes []*model.Node) (*TrafficUsecase, *fakeAgent, *fakeTrafficRepo) {
	t.Helper()

	log := slog.New(slog.NewTextHandler(io.Discard, nil))
	pkgRepo := newFakePackageRepo()
	now := time.Now().UTC()
	if err := pkgRepo.CreatePackage(&model.Package{
		Meta:   model.ResourceMeta{ID: "pkg-tg", Name: model.BuiltinPackageName, CreatedAt: now, UpdatedAt: now},
		Spec:   model.PackageSpec{Version: model.BuiltinPackageVersion, Entrypoint: model.BuiltinPackageEntrypoint},
		Status: model.PackageStatus{Builtin: true},
	}); err != nil {
		t.Fatal(err)
	}

	packages, err := NewPackageUsecase(pkgRepo, &conf.Data{}, log)
	if err != nil {
		t.Fatal(err)
	}

	agent := &fakeAgent{failFor: make(map[string]bool)}
	progRepo := &fakeProgramRepo{lab: lab, nodes: nodes}
	sc := &conf.Server{RepoAddr: "0.0.0.0:50062"}
	programs := NewProgramUsecase(progRepo, agent, fakeDriver{}, packages, &fakeHistory{}, sc, log)

	trafficRepo := &fakeTrafficRepo{lab: lab, nodes: nodes}
	uc := NewTrafficUsecase(trafficRepo, programs, &fakeTrafficHistory{}, log)

	return uc, agent, trafficRepo
}

func trafficTestNodes() []*model.Node {
	return []*model.Node{
		serverNodeWithAddr("n-1", "server-1", "10.100.1.11/24"),
		{Meta: model.ResourceMeta{ID: "n-2", Name: "leaf-1"}, Spec: model.NodeSpec{Role: model.RoleLeaf}},
		serverNodeWithAddr("n-3", "server-2", "10.100.3.11/24"),
	}
}

func validTrafficSpec() model.TrafficScenarioSpec {
	return model.TrafficScenarioSpec{
		SourceServerID: "n-1",
		DestServerID:   "n-3",
		Protocol:       model.TrafficProtocolHTTP,
		Rate:           2,
		Concurrency:    1,
	}
}

func TestCreateTrafficScenario_Validation(t *testing.T) {
	nodes := trafficTestNodes()

	cases := []struct {
		name string
		edit func(*model.TrafficScenarioSpec)
	}{
		{"bad protocol", func(s *model.TrafficScenarioSpec) { s.Protocol = "quic" }},
		{"zero rate", func(s *model.TrafficScenarioSpec) { s.Rate = 0 }},
		{"negative concurrency", func(s *model.TrafficScenarioSpec) { s.Concurrency = 0 }},
		{"negative payload", func(s *model.TrafficScenarioSpec) { s.PayloadBytes = -1 }},
		{"negative duration", func(s *model.TrafficScenarioSpec) { s.Duration = -time.Second }},
		{"bad assertion metric", func(s *model.TrafficScenarioSpec) {
			s.Assertions = []model.TrafficAssertion{{Metric: "bogus", Comparator: model.TrafficComparatorGTE, Threshold: 1}}
		}},
		{"bad assertion comparator", func(s *model.TrafficScenarioSpec) {
			s.Assertions = []model.TrafficAssertion{{Metric: model.TrafficMetricRate, Comparator: "eq", Threshold: 1}}
		}},
		{"unknown source server", func(s *model.TrafficScenarioSpec) { s.SourceServerID = "nosuch" }},
		{"unknown dest server", func(s *model.TrafficScenarioSpec) { s.DestServerID = "nosuch" }},
		{"dest not a server", func(s *model.TrafficScenarioSpec) { s.DestServerID = "n-2" }},
	}

	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			uc, _, _ := testTrafficUsecase(t, deployedLab(), nodes)
			spec := validTrafficSpec()
			c.edit(&spec)

			if _, err := uc.CreateTrafficScenario("lab-1", "http-test", spec); err == nil {
				t.Fatal("expected error")
			}
		})
	}

	t.Run("invalid name", func(t *testing.T) {
		uc, _, _ := testTrafficUsecase(t, deployedLab(), nodes)

		if _, err := uc.CreateTrafficScenario("lab-1", "Bad_Name", validTrafficSpec()); err == nil {
			t.Fatal("expected error")
		}
	})

	t.Run("success denormalises server names", func(t *testing.T) {
		uc, _, _ := testTrafficUsecase(t, deployedLab(), nodes)

		s, err := uc.CreateTrafficScenario("lab-1", "http-test", validTrafficSpec())
		if err != nil {
			t.Fatal(err)
		}

		if s.Spec.SourceServerName != "server-1" || s.Spec.DestServerName != "server-2" {
			t.Errorf("names: source=%q dest=%q", s.Spec.SourceServerName, s.Spec.DestServerName)
		}

		if s.Status.Phase != model.TrafficPhaseStopped {
			t.Errorf("phase = %q, want Stopped", s.Status.Phase)
		}
	})
}

func TestStartStopTrafficScenario(t *testing.T) {
	nodes := trafficTestNodes()
	uc, agent, _ := testTrafficUsecase(t, deployedLab(), nodes)

	s, err := uc.CreateTrafficScenario("lab-1", "http-test", validTrafficSpec())
	if err != nil {
		t.Fatal(err)
	}

	started, err := uc.StartTrafficScenario(context.Background(), "lab-1", s.Meta.ID)
	if err != nil {
		t.Fatal(err)
	}

	if started.Status.Phase != model.TrafficPhaseRunning {
		t.Fatalf("phase = %q, want Running", started.Status.Phase)
	}

	if started.Status.ServerProgramName != "http-test-server" || started.Status.ClientProgramName != "http-test-client" {
		t.Errorf("program names: server=%q client=%q", started.Status.ServerProgramName, started.Status.ClientProgramName)
	}

	if len(agent.started) != 2 {
		t.Fatalf("agent.started = %v, want 2 programs started", agent.started)
	}

	// The trafficgen binary dispatches on os.Args[1] (a "-server"/
	// "-client" mode name); regression coverage for a bug where the
	// registered Args carried only flags and no mode, so every real
	// process crashed with "unknown mode".
	var serverArgsGot, clientArgsGot []string
	for _, p := range agent.registered {
		switch p.Meta.Name {
		case "http-test-server":
			serverArgsGot = p.Spec.Args
		case "http-test-client":
			clientArgsGot = p.Spec.Args
		}
	}

	if len(serverArgsGot) == 0 || serverArgsGot[0] != "http-server" {
		t.Errorf("server program args = %v, want first element %q", serverArgsGot, "http-server")
	}

	if len(clientArgsGot) == 0 || clientArgsGot[0] != "http-client" {
		t.Errorf("client program args = %v, want first element %q", clientArgsGot, "http-client")
	}

	// Idempotent: starting again does not create more programs.
	if _, err := uc.StartTrafficScenario(context.Background(), "lab-1", s.Meta.ID); err != nil {
		t.Fatal(err)
	}

	if len(agent.started) != 2 {
		t.Errorf("agent.started = %v, want still 2 (idempotent start)", agent.started)
	}

	stopped, err := uc.StopTrafficScenario(context.Background(), "lab-1", s.Meta.ID)
	if err != nil {
		t.Fatal(err)
	}

	if stopped.Status.Phase != model.TrafficPhaseStopped {
		t.Fatalf("phase = %q, want Stopped", stopped.Status.Phase)
	}

	if len(agent.stopped) != 2 {
		t.Errorf("agent.stopped = %v, want 2 programs stopped", agent.stopped)
	}

	// Idempotent: stopping again does not stop anything more.
	if _, err := uc.StopTrafficScenario(context.Background(), "lab-1", s.Meta.ID); err != nil {
		t.Fatal(err)
	}

	if len(agent.stopped) != 2 {
		t.Errorf("agent.stopped = %v, want still 2 (idempotent stop)", agent.stopped)
	}
}

func TestStartTrafficScenario_RollsBackServerOnClientFailure(t *testing.T) {
	nodes := trafficTestNodes()
	uc, agent, repo := testTrafficUsecase(t, deployedLab(), nodes)

	s, err := uc.CreateTrafficScenario("lab-1", "http-test", validTrafficSpec())
	if err != nil {
		t.Fatal(err)
	}

	// Pre-occupy the client program's name on the source server so its
	// creation fails after the server program already succeeded.
	repo.nodes = nodes // ensure ListNodes returns the same fixture (already true)
	if _, _, err := uc.programs.CreateProgram("lab-1", "http-test-client", model.ProgramSpec{
		PackageName: model.BuiltinPackageName, PackageVersion: model.BuiltinPackageVersion,
	}, []string{"n-1"}, false); err != nil {
		t.Fatal(err)
	}

	if _, err := uc.StartTrafficScenario(context.Background(), "lab-1", s.Meta.ID); err == nil {
		t.Fatal("expected error from client program name collision")
	}

	if len(agent.removed) != 1 || agent.removed[0] != "http-test-server" {
		t.Errorf("agent.removed = %v, want rollback of the server program", agent.removed)
	}
}

func TestDeleteTrafficScenario(t *testing.T) {
	nodes := trafficTestNodes()
	uc, agent, repo := testTrafficUsecase(t, deployedLab(), nodes)

	s, err := uc.CreateTrafficScenario("lab-1", "http-test", validTrafficSpec())
	if err != nil {
		t.Fatal(err)
	}

	if _, err := uc.StartTrafficScenario(context.Background(), "lab-1", s.Meta.ID); err != nil {
		t.Fatal(err)
	}

	if err := uc.DeleteTrafficScenario(context.Background(), "lab-1", s.Meta.ID); err != nil {
		t.Fatal(err)
	}

	if len(agent.removed) != 2 {
		t.Errorf("agent.removed = %v, want both programs removed", agent.removed)
	}

	if len(repo.scenarios) != 0 {
		t.Errorf("scenarios = %v, want the scenario deleted", repo.scenarios)
	}
}

func TestTrafficScenarioHistory_InvalidRange(t *testing.T) {
	nodes := trafficTestNodes()
	uc, _, _ := testTrafficUsecase(t, deployedLab(), nodes)

	s, err := uc.CreateTrafficScenario("lab-1", "http-test", validTrafficSpec())
	if err != nil {
		t.Fatal(err)
	}

	end := time.Now().UTC()
	start := end.Add(time.Minute) // after end: invalid
	if _, err := uc.TrafficScenarioHistory("lab-1", s.Meta.ID, start, end); err == nil {
		t.Fatal("expected error for start after end")
	}
}

func TestClientArgsAndTarget(t *testing.T) {
	if got := clientTarget(model.TrafficProtocolHTTP, "10.0.0.1", 8080); got != "http://10.0.0.1:8080/" {
		t.Errorf("http target = %q", got)
	}

	if got := clientTarget(model.TrafficProtocolTCP, "10.0.0.1", 9000); got != "10.0.0.1:9000" {
		t.Errorf("tcp target = %q", got)
	}

	args := clientArgs(model.TrafficProtocolHTTP, "http://10.0.0.1:8080/", 2, 3, 64)
	want := []string{"http-client", "--target", "http://10.0.0.1:8080/", "--interval", "500ms", "--concurrency", "3", "--payload-bytes", "64"}
	if len(args) != len(want) {
		t.Fatalf("args = %v, want %v", args, want)
	}

	for i := range want {
		if args[i] != want[i] {
			t.Errorf("args[%d] = %q, want %q", i, args[i], want[i])
		}
	}

	svArgs := serverArgs(model.TrafficProtocolTCP, 9001)
	wantSv := []string{"tcp-server", "--listen", ":9001"}
	if len(svArgs) != len(wantSv) {
		t.Fatalf("serverArgs = %v, want %v", svArgs, wantSv)
	}

	for i := range wantSv {
		if svArgs[i] != wantSv[i] {
			t.Errorf("serverArgs[%d] = %q, want %q", i, svArgs[i], wantSv[i])
		}
	}
}

func TestTrafficPort(t *testing.T) {
	if p := trafficPort(model.TrafficProtocolHTTP, 0); p != defaultHTTPPort {
		t.Errorf("http default = %d", p)
	}

	if p := trafficPort(model.TrafficProtocolTCP, 0); p != defaultTCPPort {
		t.Errorf("tcp default = %d", p)
	}

	if p := trafficPort(model.TrafficProtocolHTTP, 9999); p != 9999 {
		t.Errorf("override = %d, want 9999", p)
	}
}

func TestValidateAssertionAndProtocol(t *testing.T) {
	if err := validateProtocol("http"); err != nil {
		t.Error(err)
	}

	if err := validateProtocol("bogus"); err == nil {
		t.Error("expected error")
	}

	if err := validateAssertion(model.TrafficAssertion{Metric: model.TrafficMetricP99, Comparator: model.TrafficComparatorLTE, Threshold: 100}); err != nil {
		t.Error(err)
	}

	if err := validateAssertion(model.TrafficAssertion{Metric: "bogus", Comparator: model.TrafficComparatorLTE}); err == nil {
		t.Error("expected error")
	}
}
