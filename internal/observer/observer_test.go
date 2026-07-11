package observer

import (
	"context"
	"log/slog"
	"os"
	"slices"
	"testing"

	"github.com/ifantsai/dcnetlab/internal/model"
	"github.com/ifantsai/dcnetlab/internal/runtime"
)

type fakeStore struct {
	labs         []*model.Lab
	nodes        map[string][]*model.Node
	links        map[string][]*model.Link
	updatedNodes []string
	updatedLabs  []string
}

func (s *fakeStore) ListLabs() ([]*model.Lab, error) { return s.labs, nil }

func (s *fakeStore) ListNodes(labID string) ([]*model.Node, error) { return s.nodes[labID], nil }

func (s *fakeStore) ListLinks(labID string) ([]*model.Link, error) { return s.links[labID], nil }

func (s *fakeStore) UpdateNode(n *model.Node) error {
	s.updatedNodes = append(s.updatedNodes, n.Meta.Name)

	return nil
}

func (s *fakeStore) UpdateLab(lab *model.Lab) error {
	s.updatedLabs = append(s.updatedLabs, lab.Meta.Name)

	return nil
}

// fakeDriver serves canned container states and deep-sweep output.
type fakeDriver struct {
	runtime.NoopDriver

	states map[string]string
	exec   []byte
}

func (d *fakeDriver) NodeStates(ctx context.Context, labName string, names []string) (map[string]string, error) {
	out := make(map[string]string, len(names))
	for _, n := range names {
		state, ok := d.states[n]
		if !ok {
			state = "missing"
		}

		out[n] = state
	}

	return out, nil
}

func (d *fakeDriver) Exec(ctx context.Context, labName, nodeName string, cmd []string) ([]byte, error) {
	return d.exec, nil
}

func testLab(phase model.ResourcePhase) *model.Lab {
	return &model.Lab{Meta: model.ResourceMeta{
		ID: "lab-1", Name: "dc", Generation: 1, Phase: phase,
	}}
}

func testNode(name string, phase model.ResourcePhase) *model.Node {
	return &model.Node{Meta: model.ResourceMeta{
		ID: "node-" + name, Name: name, ObservedGeneration: 1, Phase: phase,
	}}
}

const deepOutput = `lo               UNKNOWN        00:00:00:00:00:00
eth0@if20        UP             02:42:ac:14:14:09
eth1@if21        UP             aa:c1:ab:00:00:01
eth2@if22        DOWN           aa:c1:ab:00:00:02
br0              UP             aa:c1:ab:00:00:03
__SEP__
{"ipv4Unicast":{"peers":{"10.0.0.1":{"state":"Established"},"10.0.0.3":{"state":"Active"}}}}
__SEP__
{"routesTotal":42}
__SEP__
[]
`

// testLink wires spine-1 to leaf-a so both ends have simulated
// interfaces to count.
func testLink(iface string) *model.Link {
	return &model.Link{Spec: model.LinkSpec{
		EndpointA: model.LinkEndpoint{NodeID: "node-spine-1", Interface: iface},
		EndpointB: model.LinkEndpoint{NodeID: "node-leaf-a", Interface: iface},
	}}
}

func TestSweepReconcilesDriftAndCollectsMetrics(t *testing.T) {
	store := &fakeStore{
		labs: []*model.Lab{testLab(model.PhaseRunning)},
		nodes: map[string][]*model.Node{"lab-1": {
			testNode("spine-1", model.PhaseRunning), // stays running: no write
			testNode("leaf-a", model.PhaseRunning),  // actually paused: drift
		}},
		links: map[string][]*model.Link{"lab-1": {testLink("eth1"), testLink("eth2")}},
	}

	drv := &fakeDriver{
		states: map[string]string{"spine-1": "running", "leaf-a": "paused"},
		exec:   []byte(deepOutput),
	}

	o := New(store, drv, slog.New(slog.NewTextHandler(os.Stderr, nil)))

	o.sweep(context.Background())

	leaf := store.nodes["lab-1"][1]
	if leaf.Meta.Phase != model.PhaseStopped || leaf.Status.RuntimeState != model.RuntimeStateStopped {
		t.Errorf("leaf-a not reconciled: phase=%s state=%s", leaf.Meta.Phase, leaf.Status.RuntimeState)
	}

	spine := store.nodes["lab-1"][0]
	if spine.Status.BGPEstablished != 1 || spine.Status.BGPConfigured != 2 {
		t.Errorf("bgp: got %d/%d, want 1/2", spine.Status.BGPEstablished, spine.Status.BGPConfigured)
	}

	if spine.Status.RouteCount != 42 {
		t.Errorf("routes: got %d, want 42", spine.Status.RouteCount)
	}

	// Only the simulated link interfaces count: br0, lo and eth0 are
	// container plumbing outside the topology model.
	if spine.Status.InterfacesUp != 1 || spine.Status.InterfacesTotal != 2 {
		t.Errorf("interfaces: got %d/%d, want 1/2 (simulated interfaces only)",
			spine.Status.InterfacesUp, spine.Status.InterfacesTotal)
	}

	wantIfaces := []model.InterfaceStatus{{Name: "eth1", Up: true}, {Name: "eth2", Up: false}}
	if !slices.Equal(spine.Status.Interfaces, wantIfaces) {
		t.Errorf("interface detail: got %+v, want %+v", spine.Status.Interfaces, wantIfaces)
	}

	// Mixed running/stopped devices degrade the lab.
	if got := store.labs[0].Meta.Phase; got != model.PhaseDegraded {
		t.Errorf("lab phase: got %s, want Degraded", got)
	}

	if len(store.updatedLabs) != 1 {
		t.Errorf("lab updates: got %v", store.updatedLabs)
	}

	// A second identical sweep must be a no-op write-wise.
	writes := len(store.updatedNodes)
	o.sweep(context.Background())
	if len(store.updatedNodes) != writes {
		t.Errorf("unchanged sweep persisted nodes again: %v", store.updatedNodes[writes:])
	}
}

func TestSweepSkipsUndeployedLabsAndNodes(t *testing.T) {
	pending := testLab(model.PhasePending)
	pending.Meta.Generation = 0
	planned := testNode("new-leaf", model.PhasePending)
	planned.Meta.ObservedGeneration = 0

	store := &fakeStore{
		labs: []*model.Lab{pending, testLab(model.PhaseRunning)},
		nodes: map[string][]*model.Node{"lab-1": {
			testNode("spine-1", model.PhaseRunning),
			planned, // no container yet: must not be marked Failed
		}},
	}

	drv := &fakeDriver{states: map[string]string{"spine-1": "running"}, exec: []byte(deepOutput)}
	o := New(store, drv, slog.New(slog.NewTextHandler(os.Stderr, nil)))

	o.sweep(context.Background())

	if planned.Meta.Phase != model.PhasePending {
		t.Errorf("planned node phase changed to %s", planned.Meta.Phase)
	}

	if got := o.Latest("lab-1"); len(got) != 1 || got[0].Name != "spine-1" {
		t.Errorf("latest: got %+v", got)
	}
}

func TestSubscribeReceivesSweeps(t *testing.T) {
	store := &fakeStore{
		labs:  []*model.Lab{testLab(model.PhaseRunning)},
		nodes: map[string][]*model.Node{"lab-1": {testNode("spine-1", model.PhaseRunning)}},
	}

	drv := &fakeDriver{states: map[string]string{"spine-1": "running"}, exec: []byte(deepOutput)}
	o := New(store, drv, slog.New(slog.NewTextHandler(os.Stderr, nil)))

	ch, cancel := o.Subscribe("lab-1")
	defer cancel()

	o.sweep(context.Background())

	select {
	case obs := <-ch:
		if len(obs) != 1 || obs[0].RuntimeState != model.RuntimeStateRunning {
			t.Errorf("observation: %+v", obs)
		}
	default:
		t.Fatal("no observation broadcast")
	}
}
