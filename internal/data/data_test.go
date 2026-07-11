package data

import (
	"errors"
	"net/netip"
	"testing"
	"time"

	"github.com/ifantsai/dcnetlab/internal/biz"
	"github.com/ifantsai/dcnetlab/internal/model"
)

func openTest(t *testing.T) *Data {
	t.Helper()
	s, err := open(":memory:")
	if err != nil {
		t.Fatal(err)
	}

	t.Cleanup(func() { _ = s.Close() })

	return s
}

func newLab(name string) *model.Lab {
	return &model.Lab{
		Meta: model.ResourceMeta{
			ID: model.NewID("lab"), Name: name,
			Phase: model.PhasePending, CreatedAt: time.Now().UTC(),
		},
		Spec: model.LabSpec{Profile: model.ProfileMicro},
	}
}

func TestLabCRUD(t *testing.T) {
	s := openTest(t)
	lab := newLab("test")
	if err := s.CreateLab(lab); err != nil {
		t.Fatal(err)
	}

	got, err := s.GetLab(lab.Meta.ID)
	if err != nil {
		t.Fatal(err)
	}

	if got.Meta.Name != "test" || got.Spec.Profile != model.ProfileMicro {
		t.Errorf("roundtrip mismatch: %+v", got)
	}

	got.Meta.Phase = model.PhaseRunning
	if err := s.UpdateLab(got); err != nil {
		t.Fatal(err)
	}

	labs, err := s.ListLabs()
	if err != nil || len(labs) != 1 || labs[0].Meta.Phase != model.PhaseRunning {
		t.Fatalf("list: %v %+v", err, labs)
	}

	if err := s.DeleteLab(lab.Meta.ID); err != nil {
		t.Fatal(err)
	}

	if _, err := s.GetLab(lab.Meta.ID); !errors.Is(err, biz.ErrNotFound) {
		t.Errorf("expected biz.ErrNotFound, got %v", err)
	}
}

func TestDuplicateLabName(t *testing.T) {
	s := openTest(t)
	if err := s.CreateLab(newLab("dup")); err != nil {
		t.Fatal(err)
	}

	if err := s.CreateLab(newLab("dup")); err == nil {
		t.Error("duplicate name should fail")
	}
}

func TestTopologyRoundtrip(t *testing.T) {
	s := openTest(t)
	lab := newLab("topo")
	if err := s.CreateLab(lab); err != nil {
		t.Fatal(err)
	}

	node := &model.Node{
		Meta: model.ResourceMeta{ID: model.NewID("node"), Name: "leaf-1"},
		Spec: model.NodeSpec{
			LabID: lab.Meta.ID, Role: model.RoleLeaf, ASN: 4200000000,
			Loopback: netip.MustParsePrefix("10.255.0.1/32"), RuntimeType: model.RuntimeFRR,
		},
	}

	link := &model.Link{
		Meta: model.ResourceMeta{ID: model.NewID("link"), Name: "l"},
		Spec: model.LinkSpec{
			LabID:     lab.Meta.ID,
			EndpointA: model.LinkEndpoint{NodeID: node.Meta.ID, NodeName: "leaf-1", Interface: "eth1", Address: netip.MustParsePrefix("10.0.0.0/31")},
			EndpointB: model.LinkEndpoint{NodeID: "x", NodeName: "spine-1", Interface: "eth1", Address: netip.MustParsePrefix("10.0.0.1/31")},
		},
	}

	allocs := []model.Allocation{{Pool: "loopback", Value: "10.255.0.1/32", Owner: "leaf-1"}}
	if err := s.ReplaceTopology(lab.Meta.ID, []*model.Node{node}, []*model.Link{link}, allocs); err != nil {
		t.Fatal(err)
	}

	nodes, err := s.ListNodes(lab.Meta.ID)
	if err != nil || len(nodes) != 1 {
		t.Fatalf("nodes: %v %d", err, len(nodes))
	}

	if nodes[0].Spec.Loopback.String() != "10.255.0.1/32" || nodes[0].Spec.ASN != 4200000000 {
		t.Errorf("node roundtrip: %+v", nodes[0].Spec)
	}

	links, err := s.ListLinks(lab.Meta.ID)
	if err != nil || len(links) != 1 {
		t.Fatalf("links: %v %d", err, len(links))
	}

	if links[0].Spec.EndpointA.Address.String() != "10.0.0.0/31" {
		t.Errorf("link roundtrip: %+v", links[0].Spec)
	}

	got, err := s.ListAllocations(lab.Meta.ID)
	if err != nil || len(got) != 1 || got[0].Value != "10.255.0.1/32" {
		t.Fatalf("allocations: %v %+v", err, got)
	}

	// Replace wipes previous topology.
	if err := s.ReplaceTopology(lab.Meta.ID, nil, nil, nil); err != nil {
		t.Fatal(err)
	}

	nodes, _ = s.ListNodes(lab.Meta.ID)
	if len(nodes) != 0 {
		t.Errorf("expected empty topology, got %d nodes", len(nodes))
	}
}

func TestOperationLifecycle(t *testing.T) {
	s := openTest(t)
	op := &model.Operation{
		ID: model.NewID("op"), LabID: "lab-1", Type: model.OperationApplyPlan,
		State: model.OperationQueued, CreatedAt: time.Now().UTC(),
	}

	if err := s.CreateOperation(op); err != nil {
		t.Fatal(err)
	}

	op.State = model.OperationSucceeded
	op.Progress = 100
	if err := s.UpdateOperation(op); err != nil {
		t.Fatal(err)
	}

	got, err := s.GetOperation(op.ID)
	if err != nil || got.State != model.OperationSucceeded {
		t.Fatalf("got %v %+v", err, got)
	}

	ops, err := s.ListOperations("lab-1")
	if err != nil || len(ops) != 1 {
		t.Fatalf("list: %v %d", err, len(ops))
	}
}

func TestGenerationRetention(t *testing.T) {
	s := openTest(t)
	lab := newLab("gen")
	for g := int64(1); g <= 13; g++ {
		if err := s.SaveGeneration(lab.Meta.ID, g, &biz.DesiredStateSnapshot{Lab: lab}); err != nil {
			t.Fatal(err)
		}
	}

	gens, err := s.ListGenerations(lab.Meta.ID)
	if err != nil {
		t.Fatal(err)
	}

	if len(gens) != 10 || gens[0] != 13 || gens[9] != 4 {
		t.Errorf("retention: got %v", gens)
	}

	if _, err := s.GetGeneration(lab.Meta.ID, 13); err != nil {
		t.Errorf("latest generation must exist: %v", err)
	}

	if _, err := s.GetGeneration(lab.Meta.ID, 1); !errors.Is(err, biz.ErrNotFound) {
		t.Errorf("pruned generation should be gone, got %v", err)
	}
}
