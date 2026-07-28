package biz

import (
	"context"
	"errors"
	"io"
	"log/slog"
	"testing"

	"github.com/ifantsai/dcnetlab/internal/model"
	"github.com/ifantsai/dcnetlab/internal/runtime"
)

var errNotSupportedForTest = errors.New("interface state change refused")

// fakeFaultRepo keeps labs, nodes, links and scenarios in memory.
type fakeFaultRepo struct {
	lab       *model.Lab
	nodes     []*model.Node
	links     []*model.Link
	scenarios []*model.FaultScenario
}

func (r *fakeFaultRepo) GetLab(id string) (*model.Lab, error) {
	if r.lab == nil || r.lab.Meta.ID != id {
		return nil, ErrNotFound
	}

	return r.lab, nil
}

func (r *fakeFaultRepo) ListNodes(string) ([]*model.Node, error) { return r.nodes, nil }
func (r *fakeFaultRepo) ListLinks(string) ([]*model.Link, error) { return r.links, nil }

// UpdateLab/UpdateNode satisfy PowerRepo (the fault usecase drives
// node-stop/node-restart through PowerUsecase); the tests only assert
// on driver calls and FaultScenario status, not on lab/node phase.
func (r *fakeFaultRepo) UpdateLab(*model.Lab) error   { return nil }
func (r *fakeFaultRepo) UpdateNode(*model.Node) error { return nil }

func (r *fakeFaultRepo) CreateFaultScenario(s *model.FaultScenario) error {
	r.scenarios = append(r.scenarios, s)

	return nil
}

func (r *fakeFaultRepo) UpdateFaultScenario(*model.FaultScenario) error { return nil }

func (r *fakeFaultRepo) DeleteFaultScenario(id string) error {
	for i, s := range r.scenarios {
		if s.Meta.ID == id {
			r.scenarios = append(r.scenarios[:i], r.scenarios[i+1:]...)

			return nil
		}
	}

	return ErrNotFound
}

func (r *fakeFaultRepo) GetFaultScenario(id string) (*model.FaultScenario, error) {
	for _, s := range r.scenarios {
		if s.Meta.ID == id {
			return s, nil
		}
	}

	return nil, ErrNotFound
}

func (r *fakeFaultRepo) ListFaultScenarios(labID string) ([]*model.FaultScenario, error) {
	var out []*model.FaultScenario
	for _, s := range r.scenarios {
		if s.Spec.LabID == labID {
			out = append(out, s)
		}
	}

	return out, nil
}

// faultCall records one exec-style invocation against the fake
// driver, so tests can assert exactly which node/interface a fault
// touched without caring about the underlying shell command.
type faultCall struct {
	op    string // "iface-up", "iface-down", "impair", "clear-impair"
	node  string
	iface string
	imp   runtime.Impairment
}

// fakeFaultDriver implements the slice of runtime.Driver the fault
// usecase exercises directly (node power goes through PowerUsecase,
// which uses StartNodes/StopNodes); everything else panics if called,
// which the tests below never do.
type fakeFaultDriver struct {
	runtime.Driver

	calls      []faultCall
	stateFail  map[string]error
	nodeStates map[string]bool // node -> paused
}

func (d *fakeFaultDriver) StartNodes(ctx context.Context, labName string, names []string) error {
	if d.nodeStates == nil {
		d.nodeStates = map[string]bool{}
	}

	for _, n := range names {
		d.nodeStates[n] = false
	}

	return nil
}

func (d *fakeFaultDriver) StopNodes(ctx context.Context, labName string, names []string) error {
	if d.nodeStates == nil {
		d.nodeStates = map[string]bool{}
	}

	for _, n := range names {
		d.nodeStates[n] = true
	}

	return nil
}

func (d *fakeFaultDriver) SetInterfaceState(ctx context.Context, labName, nodeName, iface string, up bool) error {
	if err := d.stateFail[nodeName+":"+iface]; err != nil {
		return err
	}

	op := "iface-down"
	if up {
		op = "iface-up"
	}

	d.calls = append(d.calls, faultCall{op: op, node: nodeName, iface: iface})

	return nil
}

func (d *fakeFaultDriver) ApplyImpairment(ctx context.Context, labName, nodeName, iface string, imp runtime.Impairment) error {
	d.calls = append(d.calls, faultCall{op: "impair", node: nodeName, iface: iface, imp: imp})

	return nil
}

func (d *fakeFaultDriver) ClearImpairment(ctx context.Context, labName, nodeName, iface string) error {
	d.calls = append(d.calls, faultCall{op: "clear-impair", node: nodeName, iface: iface})

	return nil
}

func testLog() *slog.Logger { return slog.New(slog.NewTextHandler(io.Discard, nil)) }

// setupFault builds a fault usecase against a lab with one leaf, one
// server and a server-access link between them; the lab has a
// deployed generation so power/link operations are allowed.
func setupFault(t *testing.T) (*FaultUsecase, *fakeFaultRepo, *fakeFaultDriver, *model.Node, *model.Node, *model.Link) {
	t.Helper()

	lab := &model.Lab{Meta: model.ResourceMeta{ID: "lab1", Name: "lab1", Generation: 1}}
	leaf := &model.Node{Meta: model.ResourceMeta{ID: "leaf1", Name: "leaf1"}, Spec: model.NodeSpec{Role: model.RoleLeaf}}
	server := &model.Node{Meta: model.ResourceMeta{ID: "server1", Name: "server1"}, Spec: model.NodeSpec{Role: model.RoleServer}}
	link := &model.Link{
		Meta: model.ResourceMeta{ID: "link1", Name: "leaf1--server1"},
		Spec: model.LinkSpec{
			LabID:     lab.Meta.ID,
			Kind:      model.LinkServerAccess,
			EndpointA: model.LinkEndpoint{NodeID: leaf.Meta.ID, NodeName: leaf.Meta.Name, Interface: "eth3"},
			EndpointB: model.LinkEndpoint{NodeID: server.Meta.ID, NodeName: server.Meta.Name, Interface: "eth1"},
		},
	}

	repo := &fakeFaultRepo{lab: lab, nodes: []*model.Node{leaf, server}, links: []*model.Link{link}}
	driver := &fakeFaultDriver{stateFail: map[string]error{}}
	power := NewPowerUsecase(repo, nil, driver, testLog())
	uc := NewFaultUsecase(repo, power, driver, testLog())

	return uc, repo, driver, leaf, server, link
}

func TestCreateFaultScenario_Validation(t *testing.T) {
	uc, _, _, leaf, _, link := setupFault(t)

	tests := []struct {
		name    string
		spec    model.FaultScenarioSpec
		wantErr bool
	}{
		{
			name:    "valid node-stop",
			spec:    model.FaultScenarioSpec{Target: model.FaultTarget{NodeID: leaf.Meta.ID}, Type: model.FaultNodeStop},
			wantErr: false,
		},
		{
			name:    "unknown type",
			spec:    model.FaultScenarioSpec{Target: model.FaultTarget{NodeID: leaf.Meta.ID}, Type: "bogus"},
			wantErr: true,
		},
		{
			name:    "node-stop with side is rejected",
			spec:    model.FaultScenarioSpec{Target: model.FaultTarget{NodeID: leaf.Meta.ID, Side: model.FaultSideA}, Type: model.FaultNodeStop},
			wantErr: true,
		},
		{
			name:    "unknown node",
			spec:    model.FaultScenarioSpec{Target: model.FaultTarget{NodeID: "nope"}, Type: model.FaultNodeStop},
			wantErr: true,
		},
		{
			name:    "interface-down without side is rejected",
			spec:    model.FaultScenarioSpec{Target: model.FaultTarget{LinkID: link.Meta.ID}, Type: model.FaultInterfaceDown},
			wantErr: true,
		},
		{
			name:    "interface-down with valid side",
			spec:    model.FaultScenarioSpec{Target: model.FaultTarget{LinkID: link.Meta.ID, Side: model.FaultSideA}, Type: model.FaultInterfaceDown},
			wantErr: false,
		},
		{
			name:    "link-down with side a is rejected: it always acts on both",
			spec:    model.FaultScenarioSpec{Target: model.FaultTarget{LinkID: link.Meta.ID, Side: model.FaultSideA}, Type: model.FaultLinkDown},
			wantErr: true,
		},
		{
			name:    "impairment without any parameter is rejected",
			spec:    model.FaultScenarioSpec{Target: model.FaultTarget{LinkID: link.Meta.ID}, Type: model.FaultImpairment},
			wantErr: true,
		},
		{
			name: "impairment with delay is valid",
			spec: model.FaultScenarioSpec{
				Target: model.FaultTarget{LinkID: link.Meta.ID}, Type: model.FaultImpairment,
				Impairment: &model.FaultImpairmentSpec{DelayMs: 100},
			},
			wantErr: false,
		},
		{
			name: "jitter without delay is rejected",
			spec: model.FaultScenarioSpec{
				Target: model.FaultTarget{LinkID: link.Meta.ID}, Type: model.FaultImpairment,
				Impairment: &model.FaultImpairmentSpec{JitterMs: 10},
			},
			wantErr: true,
		},
		{
			name: "impairment on node-stop is rejected",
			spec: model.FaultScenarioSpec{
				Target: model.FaultTarget{NodeID: leaf.Meta.ID}, Type: model.FaultNodeStop,
				Impairment: &model.FaultImpairmentSpec{DelayMs: 100},
			},
			wantErr: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			_, err := uc.CreateFaultScenario("lab1", "test-fault", tt.spec)
			if (err != nil) != tt.wantErr {
				t.Errorf("CreateFaultScenario() error = %v, wantErr %v", err, tt.wantErr)
			}
		})
	}
}

func TestCreateFaultScenario_ResolvesTargetNames(t *testing.T) {
	uc, _, _, leaf, _, link := setupFault(t)

	nodeFault, err := uc.CreateFaultScenario("lab1", "stop-leaf", model.FaultScenarioSpec{
		Target: model.FaultTarget{NodeID: leaf.Meta.ID}, Type: model.FaultNodeStop,
	})
	if err != nil {
		t.Fatalf("CreateFaultScenario: %v", err)
	}

	if nodeFault.Spec.Target.Kind != model.FaultTargetNode || nodeFault.Spec.Target.NodeName != "leaf1" {
		t.Errorf("node target = %+v, want kind=node name=leaf1", nodeFault.Spec.Target)
	}

	linkFault, err := uc.CreateFaultScenario("lab1", "down-link", model.FaultScenarioSpec{
		Target: model.FaultTarget{LinkID: link.Meta.ID}, Type: model.FaultLinkDown,
	})
	if err != nil {
		t.Fatalf("CreateFaultScenario: %v", err)
	}

	if linkFault.Spec.Target.Kind != model.FaultTargetLink || linkFault.Spec.Target.LinkName != "leaf1--server1" {
		t.Errorf("link target = %+v, want kind=link name=leaf1--server1", linkFault.Spec.Target)
	}
}

func TestApplyRecover_LinkDown_BothEnds(t *testing.T) {
	uc, _, driver, _, _, link := setupFault(t)

	s, err := uc.CreateFaultScenario("lab1", "cut-link", model.FaultScenarioSpec{
		Target: model.FaultTarget{LinkID: link.Meta.ID}, Type: model.FaultLinkDown,
	})
	if err != nil {
		t.Fatalf("CreateFaultScenario: %v", err)
	}

	applied, err := uc.ApplyFaultScenario(context.Background(), "lab1", s.Meta.ID)
	if err != nil {
		t.Fatalf("ApplyFaultScenario: %v", err)
	}

	if !applied.Status.Applied {
		t.Fatal("want Applied = true after apply")
	}

	if len(driver.calls) != 2 || driver.calls[0].op != "iface-down" || driver.calls[1].op != "iface-down" {
		t.Fatalf("want two iface-down calls, got %+v", driver.calls)
	}

	// Idempotent re-apply: already applied, no additional calls.
	if _, err := uc.ApplyFaultScenario(context.Background(), "lab1", s.Meta.ID); err != nil {
		t.Fatalf("re-apply: %v", err)
	}

	if len(driver.calls) != 2 {
		t.Fatalf("re-apply should be a no-op, got %d calls", len(driver.calls))
	}

	recovered, err := uc.RecoverFaultScenario(context.Background(), "lab1", s.Meta.ID)
	if err != nil {
		t.Fatalf("RecoverFaultScenario: %v", err)
	}

	if recovered.Status.Applied {
		t.Fatal("want Applied = false after recover")
	}

	if len(driver.calls) != 4 || driver.calls[2].op != "iface-up" || driver.calls[3].op != "iface-up" {
		t.Fatalf("want two more iface-up calls, got %+v", driver.calls)
	}
}

func TestApplyInterfaceDown_SingleSide(t *testing.T) {
	uc, _, driver, leaf, _, link := setupFault(t)

	s, err := uc.CreateFaultScenario("lab1", "drop-a", model.FaultScenarioSpec{
		Target: model.FaultTarget{LinkID: link.Meta.ID, Side: model.FaultSideA}, Type: model.FaultInterfaceDown,
	})
	if err != nil {
		t.Fatalf("CreateFaultScenario: %v", err)
	}

	if _, err := uc.ApplyFaultScenario(context.Background(), "lab1", s.Meta.ID); err != nil {
		t.Fatalf("ApplyFaultScenario: %v", err)
	}

	if len(driver.calls) != 1 || driver.calls[0].node != leaf.Meta.Name || driver.calls[0].iface != "eth3" {
		t.Fatalf("want one iface-down call on leaf1:eth3, got %+v", driver.calls)
	}
}

func TestApplyImpairment_CombinesParameters(t *testing.T) {
	uc, _, driver, _, server, link := setupFault(t)

	s, err := uc.CreateFaultScenario("lab1", "slow-b", model.FaultScenarioSpec{
		Target: model.FaultTarget{LinkID: link.Meta.ID, Side: model.FaultSideB}, Type: model.FaultImpairment,
		Impairment: &model.FaultImpairmentSpec{DelayMs: 100, JitterMs: 10, LossPercent: 1, RateKbit: 5000},
	})
	if err != nil {
		t.Fatalf("CreateFaultScenario: %v", err)
	}

	if _, err := uc.ApplyFaultScenario(context.Background(), "lab1", s.Meta.ID); err != nil {
		t.Fatalf("ApplyFaultScenario: %v", err)
	}

	if len(driver.calls) != 1 || driver.calls[0].op != "impair" || driver.calls[0].node != server.Meta.Name {
		t.Fatalf("want one impair call on server1, got %+v", driver.calls)
	}

	want := runtime.Impairment{DelayMs: 100, JitterMs: 10, LossPercent: 1, RateKbit: 5000}
	if driver.calls[0].imp != want {
		t.Errorf("impairment params = %+v, want %+v", driver.calls[0].imp, want)
	}

	if _, err := uc.RecoverFaultScenario(context.Background(), "lab1", s.Meta.ID); err != nil {
		t.Fatalf("RecoverFaultScenario: %v", err)
	}

	if len(driver.calls) != 2 || driver.calls[1].op != "clear-impair" {
		t.Fatalf("want a clear-impair call, got %+v", driver.calls)
	}
}

func TestApplyFaultScenario_RejectsSecondFaultOnSameTarget(t *testing.T) {
	uc, _, _, _, _, link := setupFault(t)

	first, err := uc.CreateFaultScenario("lab1", "first", model.FaultScenarioSpec{
		Target: model.FaultTarget{LinkID: link.Meta.ID}, Type: model.FaultLinkDown,
	})
	if err != nil {
		t.Fatalf("CreateFaultScenario: %v", err)
	}

	second, err := uc.CreateFaultScenario("lab1", "second", model.FaultScenarioSpec{
		Target: model.FaultTarget{LinkID: link.Meta.ID, Side: model.FaultSideA}, Type: model.FaultInterfaceDown,
	})
	if err != nil {
		t.Fatalf("CreateFaultScenario: %v", err)
	}

	if _, err := uc.ApplyFaultScenario(context.Background(), "lab1", first.Meta.ID); err != nil {
		t.Fatalf("apply first: %v", err)
	}

	if _, err := uc.ApplyFaultScenario(context.Background(), "lab1", second.Meta.ID); err == nil {
		t.Fatal("want an error applying a second fault to the same link while the first is still applied")
	}
}

func TestApplyFaultScenario_RollsBackOnPartialFailure(t *testing.T) {
	uc, _, driver, leaf, server, link := setupFault(t)
	// Endpoint A (leaf1:eth3) is applied first and succeeds; endpoint B
	// (server1:eth1) then fails, so the already-applied leaf side must
	// be rolled back rather than left half-faulted.
	driver.stateFail[server.Meta.Name+":eth1"] = errNotSupportedForTest

	s, err := uc.CreateFaultScenario("lab1", "cut-link", model.FaultScenarioSpec{
		Target: model.FaultTarget{LinkID: link.Meta.ID}, Type: model.FaultLinkDown,
	})
	if err != nil {
		t.Fatalf("CreateFaultScenario: %v", err)
	}

	if _, err := uc.ApplyFaultScenario(context.Background(), "lab1", s.Meta.ID); err == nil {
		t.Fatal("want an error when one endpoint fails to apply")
	}

	if len(driver.calls) != 2 || driver.calls[0].op != "iface-down" || driver.calls[0].node != leaf.Meta.Name ||
		driver.calls[1].op != "iface-up" || driver.calls[1].node != leaf.Meta.Name {
		t.Fatalf("want down-then-rollback-up on leaf1 (the endpoint that succeeded), got %+v", driver.calls)
	}

	got, err := uc.get("lab1", s.Meta.ID)
	if err != nil {
		t.Fatalf("get: %v", err)
	}

	if got.Status.Applied {
		t.Error("a partially-applied fault must not be recorded as applied")
	}
}

func TestApplyRecover_NodeStop(t *testing.T) {
	uc, _, driver, leaf, _, _ := setupFault(t)

	s, err := uc.CreateFaultScenario("lab1", "stop-leaf", model.FaultScenarioSpec{
		Target: model.FaultTarget{NodeID: leaf.Meta.ID}, Type: model.FaultNodeStop,
	})
	if err != nil {
		t.Fatalf("CreateFaultScenario: %v", err)
	}

	if _, err := uc.ApplyFaultScenario(context.Background(), "lab1", s.Meta.ID); err != nil {
		t.Fatalf("ApplyFaultScenario: %v", err)
	}

	if !driver.nodeStates[leaf.Meta.Name] {
		t.Fatal("want leaf1 paused after node-stop apply")
	}

	if _, err := uc.RecoverFaultScenario(context.Background(), "lab1", s.Meta.ID); err != nil {
		t.Fatalf("RecoverFaultScenario: %v", err)
	}

	if driver.nodeStates[leaf.Meta.Name] {
		t.Fatal("want leaf1 resumed after recover")
	}
}

func TestApplyNodeRestart_SelfRecoversImmediately(t *testing.T) {
	uc, _, driver, leaf, _, _ := setupFault(t)

	s, err := uc.CreateFaultScenario("lab1", "restart-leaf", model.FaultScenarioSpec{
		Target: model.FaultTarget{NodeID: leaf.Meta.ID}, Type: model.FaultNodeRestart,
	})
	if err != nil {
		t.Fatalf("CreateFaultScenario: %v", err)
	}

	applied, err := uc.ApplyFaultScenario(context.Background(), "lab1", s.Meta.ID)
	if err != nil {
		t.Fatalf("ApplyFaultScenario: %v", err)
	}

	if applied.Status.Applied {
		t.Error("node-restart is a point-in-time event; it must not report Applied = true")
	}

	if driver.nodeStates[leaf.Meta.Name] {
		t.Error("want leaf1 running again after a restart")
	}

	if _, err := uc.RecoverFaultScenario(context.Background(), "lab1", s.Meta.ID); err == nil {
		t.Error("want an error recovering a node-restart: it already recovered itself")
	}
}

func TestDeleteFaultScenario_RecoversFirst(t *testing.T) {
	uc, repo, driver, _, _, link := setupFault(t)

	s, err := uc.CreateFaultScenario("lab1", "cut-link", model.FaultScenarioSpec{
		Target: model.FaultTarget{LinkID: link.Meta.ID}, Type: model.FaultLinkDown,
	})
	if err != nil {
		t.Fatalf("CreateFaultScenario: %v", err)
	}

	if _, err := uc.ApplyFaultScenario(context.Background(), "lab1", s.Meta.ID); err != nil {
		t.Fatalf("ApplyFaultScenario: %v", err)
	}

	if err := uc.DeleteFaultScenario(context.Background(), "lab1", s.Meta.ID); err != nil {
		t.Fatalf("DeleteFaultScenario: %v", err)
	}

	if len(repo.scenarios) != 0 {
		t.Fatalf("want the scenario removed, got %d left", len(repo.scenarios))
	}

	upCalls := 0
	for _, c := range driver.calls {
		if c.op == "iface-up" {
			upCalls++
		}
	}

	if upCalls != 2 {
		t.Errorf("want delete to recover both endpoints before removing the resource, got %d iface-up calls", upCalls)
	}
}
