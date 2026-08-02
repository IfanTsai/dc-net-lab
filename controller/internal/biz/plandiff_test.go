package biz

import (
	"strings"
	"testing"

	"github.com/ifantsai/dcnetlab/controller/internal/topology"
	"github.com/ifantsai/dcnetlab/internal/model"
)

func buildStandard(t *testing.T, base *topology.Base, mutate func(*model.TopologySpec)) *topology.Result {
	t.Helper()

	spec := model.LabSpec{
		Profile:  model.ProfileStandard,
		Topology: topology.Profiles()[model.ProfileStandard],
		Pools:    topology.DefaultPools(),
		ASNs:     topology.DefaultASNRanges(),
	}

	pods := make([]model.PodSpec, len(spec.Topology.Pods))
	copy(pods, spec.Topology.Pods)
	spec.Topology.Pods = pods
	if mutate != nil {
		mutate(&spec.Topology)
	}

	b, err := topology.NewBuilder("lab-test", spec, base)
	if err != nil {
		t.Fatal(err)
	}

	res, err := b.Build(spec.Topology)
	if err != nil {
		t.Fatal(err)
	}

	return res
}

func opsByType(ops []model.PlanOperation) map[model.PlanOperationType][]model.PlanOperation {
	m := make(map[model.PlanOperationType][]model.PlanOperation)
	for _, op := range ops {
		m[op.Type] = append(m[op.Type], op)
	}

	return m
}

func TestPlanChangesFreshLab(t *testing.T) {
	res := buildStandard(t, nil, nil)

	ops, warnings := planChanges(nil, res, nil)
	byType := opsByType(ops)

	if got := len(byType[model.PlanCreateNode]); got != len(res.Nodes) {
		t.Errorf("create-node ops: got %d, want %d", got, len(res.Nodes))
	}

	if got := len(byType[model.PlanCreateLink]); got != len(res.Links) {
		t.Errorf("create-link ops: got %d, want %d", got, len(res.Links))
	}

	if len(byType[model.PlanUpdateNode])+len(byType[model.PlanDeleteNode])+len(byType[model.PlanDeleteLink]) != 0 {
		t.Errorf("fresh lab produced update/delete ops: %v", ops)
	}

	if len(warnings) != 0 {
		t.Errorf("fresh lab produced warnings: %v", warnings)
	}
}

func TestPlanChangesScaleOutRack(t *testing.T) {
	baseRes := buildStandard(t, nil, nil)
	base := &topology.Base{Nodes: baseRes.Nodes, Links: baseRes.Links}

	res := buildStandard(t, base, func(spec *model.TopologySpec) { spec.Pods[0].Racks = 3 })

	ops, warnings := planChanges(base, res, nil)
	byType := opsByType(ops)

	// One rack: 2 leaves + 2 servers.
	if got := len(byType[model.PlanCreateNode]); got != 4 {
		t.Errorf("create-node ops: got %d, want 4: %v", got, byType[model.PlanCreateNode])
	}

	// 2 spines x 2 leaves fabric + 1 mlag peer + 2 servers x 2 access.
	if got := len(byType[model.PlanCreateLink]); got != 9 {
		t.Errorf("create-link ops: got %d, want 9", got)
	}

	// Only pod-1's spines carry new links to the new rack.
	updates := make(map[string]bool)
	for _, op := range byType[model.PlanUpdateNode] {
		updates[op.Target] = true
	}

	if len(updates) != 2 || !updates["pod-1-spine-1"] || !updates["pod-1-spine-2"] {
		t.Errorf("update-node targets: got %v, want pod-1's spines", updates)
	}

	if len(byType[model.PlanDeleteNode])+len(byType[model.PlanDeleteLink]) != 0 {
		t.Error("scale-out produced delete ops")
	}

	if len(warnings) != 0 {
		t.Errorf("unexpected warnings: %v", warnings)
	}
}

func TestPlanChangesScaleInWarnsAboutPrograms(t *testing.T) {
	baseRes := buildStandard(t, nil, nil)
	base := &topology.Base{Nodes: baseRes.Nodes, Links: baseRes.Links}

	res := buildStandard(t, base, func(spec *model.TopologySpec) { spec.Pods[1].Racks = 1 })

	programs := []*model.Program{
		{Meta: model.ResourceMeta{Name: "web"}, Spec: model.ProgramSpec{ServerName: "pod-2-rack-4-server-1"}},
		{Meta: model.ResourceMeta{Name: "db"}, Spec: model.ProgramSpec{ServerName: "pod-2-rack-4-server-1"}},
		{Meta: model.ResourceMeta{Name: "survivor"}, Spec: model.ProgramSpec{ServerName: "pod-1-rack-1-server-1"}},
	}

	ops, warnings := planChanges(base, res, programs)
	byType := opsByType(ops)

	if got := len(byType[model.PlanDeleteNode]); got != 4 {
		t.Errorf("delete-node ops: got %d, want 4", got)
	}

	if got := len(byType[model.PlanDeleteLink]); got != 9 {
		t.Errorf("delete-link ops: got %d, want 9", got)
	}

	if len(byType[model.PlanCreateNode])+len(byType[model.PlanCreateLink]) != 0 {
		t.Error("scale-in produced create ops")
	}

	if len(warnings) != 1 {
		t.Fatalf("warnings: got %v, want one for the removed server", warnings)
	}

	msg := warnings[0].Message
	if !strings.Contains(msg, "pod-2-rack-4-server-1") ||
		!strings.Contains(msg, "db, web") || strings.Contains(msg, "survivor") {
		t.Errorf("warning message: %s", msg)
	}
}

func TestPlanChangesNoChange(t *testing.T) {
	baseRes := buildStandard(t, nil, nil)
	base := &topology.Base{Nodes: baseRes.Nodes, Links: baseRes.Links}

	res := buildStandard(t, base, nil)

	ops, warnings := planChanges(base, res, nil)
	if len(ops) != 0 || len(warnings) != 0 {
		t.Errorf("no-change plan produced %v / %v", ops, warnings)
	}
}

func TestCarryOverIdentity(t *testing.T) {
	res := buildStandard(t, nil, nil)

	// Simulate the stored desired state: same names, different IDs and
	// an observed status.
	var stored []*model.Node
	for _, n := range res.Nodes {
		cp := *n
		cp.Meta.ID = "node-stored-" + n.Meta.Name
		cp.Meta.Phase = model.PhaseRunning
		cp.Status.BGPEstablished = 3
		stored = append(stored, &cp)
	}

	var storedLinks []*model.Link
	for _, l := range res.Links {
		cp := *l
		cp.Meta.ID = "link-stored-" + l.Meta.Name
		storedLinks = append(storedLinks, &cp)
	}

	carryOverIdentity(res, stored, storedLinks)

	for _, n := range res.Nodes {
		if n.Meta.ID != "node-stored-"+n.Meta.Name {
			t.Fatalf("node %s: id not carried over: %s", n.Meta.Name, n.Meta.ID)
		}

		if n.Meta.Phase != model.PhaseRunning || n.Status.BGPEstablished != 3 {
			t.Fatalf("node %s: observed state not carried over", n.Meta.Name)
		}
	}

	for _, l := range res.Links {
		if l.Meta.ID != "link-stored-"+l.Meta.Name {
			t.Fatalf("link %s: id not carried over: %s", l.Meta.Name, l.Meta.ID)
		}

		if want := "node-stored-" + l.Spec.EndpointA.NodeName; l.Spec.EndpointA.NodeID != want {
			t.Fatalf("link %s: endpoint node id not rewritten: %s", l.Meta.Name, l.Spec.EndpointA.NodeID)
		}
	}
}
