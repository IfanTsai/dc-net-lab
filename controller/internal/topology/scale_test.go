package topology

import (
	"testing"

	"github.com/ifantsai/dcnetlab/internal/model"
)

// standardSpec returns a fresh standard-profile lab spec; the pod
// slice is copied so tests can mutate counts independently.
func standardSpec() model.LabSpec {
	spec := model.LabSpec{
		Profile:  model.ProfileStandard,
		Topology: Profiles()[model.ProfileStandard],
		Pools:    DefaultPools(),
		ASNs:     DefaultASNRanges(),
	}

	pods := make([]model.PodSpec, len(spec.Topology.Pods))
	copy(pods, spec.Topology.Pods)
	spec.Topology.Pods = pods

	return spec
}

func rebuild(t *testing.T, spec model.LabSpec, base *Base) *Result {
	t.Helper()

	b, err := NewBuilder("lab-test", spec, base)
	if err != nil {
		t.Fatal(err)
	}

	res, err := b.Build(spec.Topology)
	if err != nil {
		t.Fatal(err)
	}

	return res
}

// assertSurvivorsUnchanged checks that every base node and link still
// present by name in the rebuilt result kept its addressing, ASN and
// interface assignment bit-for-bit.
func assertSurvivorsUnchanged(t *testing.T, base *Base, res *Result) {
	t.Helper()

	resNodes := make(map[string]*model.Node)
	for _, n := range res.Nodes {
		resNodes[n.Meta.Name] = n
	}

	resLinks := make(map[string]*model.Link)
	for _, l := range res.Links {
		resLinks[l.Meta.Name] = l
	}

	for _, old := range base.Nodes {
		n, ok := resNodes[old.Meta.Name]
		if !ok {
			continue
		}

		if n.Spec.ASN != old.Spec.ASN {
			t.Errorf("%s: asn %d -> %d", old.Meta.Name, old.Spec.ASN, n.Spec.ASN)
		}

		if n.Spec.Loopback != old.Spec.Loopback {
			t.Errorf("%s: loopback %s -> %s", old.Meta.Name, old.Spec.Loopback, n.Spec.Loopback)
		}

		if n.Spec.VlanIP != old.Spec.VlanIP {
			t.Errorf("%s: vlanif %s -> %s", old.Meta.Name, old.Spec.VlanIP, n.Spec.VlanIP)
		}

		if n.Spec.Address != old.Spec.Address {
			t.Errorf("%s: address %s -> %s", old.Meta.Name, old.Spec.Address, n.Spec.Address)
		}

		if n.Spec.RackID != old.Spec.RackID {
			t.Errorf("%s: rack %s -> %s", old.Meta.Name, old.Spec.RackID, n.Spec.RackID)
		}
	}

	for _, old := range base.Links {
		l, ok := resLinks[old.Meta.Name]
		if !ok {
			continue
		}

		for i, eps := range [][2]model.LinkEndpoint{
			{old.Spec.EndpointA, l.Spec.EndpointA},
			{old.Spec.EndpointB, l.Spec.EndpointB},
		} {
			if eps[0].Interface != eps[1].Interface {
				t.Errorf("%s endpoint %d: interface %s -> %s", old.Meta.Name, i, eps[0].Interface, eps[1].Interface)
			}

			if eps[0].Address != eps[1].Address {
				t.Errorf("%s endpoint %d: address %s -> %s", old.Meta.Name, i, eps[0].Address, eps[1].Address)
			}
		}
	}
}

func linkByName(res *Result, name string) *model.Link {
	for _, l := range res.Links {
		if l.Meta.Name == name {
			return l
		}
	}

	return nil
}

func nodeByName(res *Result, name string) *model.Node {
	for _, n := range res.Nodes {
		if n.Meta.Name == name {
			return n
		}
	}

	return nil
}

// TestScaleOutAddRack grows pod-1 from 2 to 3 racks: the new rack must
// take the next never-used global number (5, past pod-2's rack-4)
// instead of renumbering pod-2, and new spine downlinks must land on
// fresh eth indices.
func TestScaleOutAddRack(t *testing.T) {
	spec := standardSpec()
	baseRes := rebuild(t, spec, nil)
	base := &Base{Nodes: baseRes.Nodes, Links: baseRes.Links}

	spec.Topology.Pods[0].Racks = 3
	res := rebuild(t, spec, base)

	assertSurvivorsUnchanged(t, base, res)

	leaf := nodeByName(res, "pod-1-rack-5-leaf-a")
	if leaf == nil {
		t.Fatalf("new rack not numbered 5: %v", nodeNames(res, model.RoleLeaf))
	}

	if want := LeafRackASNBase + 5; leaf.Spec.ASN != want {
		t.Errorf("new rack asn: got %d, want %d", leaf.Spec.ASN, want)
	}

	if got := leaf.Spec.VlanIP.Masked().String(); got != "10.100.4.0/24" {
		t.Errorf("new rack subnet: got %s, want 10.100.4.0/24", got)
	}

	// pod-1-spine-1 had eth1-2 towards the superspines and eth3-6
	// towards racks 1 and 2; the new rack must extend, not reuse.
	l := linkByName(res, "pod-1-spine-1--pod-1-rack-5-leaf-a")
	if l == nil {
		t.Fatal("missing spine link to new rack")
	}

	if l.Spec.EndpointA.Interface != "eth7" {
		t.Errorf("new spine downlink on %s, want eth7", l.Spec.EndpointA.Interface)
	}
}

// TestScaleOutAddPod appends a pod: its spines take the next spine
// ASNs and every superspine grows fresh downlink interfaces.
func TestScaleOutAddPod(t *testing.T) {
	spec := standardSpec()
	baseRes := rebuild(t, spec, nil)
	base := &Base{Nodes: baseRes.Nodes, Links: baseRes.Links}

	spec.Topology.Pods = append(spec.Topology.Pods, model.PodSpec{
		Name: "pod-3", Spines: 2, Racks: 1, ServersPerRack: 2,
	})
	res := rebuild(t, spec, base)

	assertSurvivorsUnchanged(t, base, res)

	spine := nodeByName(res, "pod-3-spine-1")
	if spine == nil {
		t.Fatal("missing pod-3 spine")
	}

	if spine.Spec.ASN != 65204 {
		t.Errorf("pod-3 spine asn: got %d, want 65204", spine.Spec.ASN)
	}

	if nodeByName(res, "pod-3-rack-5-leaf-a") == nil {
		t.Errorf("pod-3 rack not numbered 5: %v", nodeNames(res, model.RoleLeaf))
	}

	// superspine-1 had eth1-2 from the dcedges and eth3-6 towards the
	// four existing pod spines.
	l := linkByName(res, "superspine-1--pod-3-spine-1")
	if l == nil {
		t.Fatal("missing superspine link to pod-3")
	}

	if l.Spec.EndpointA.Interface != "eth7" {
		t.Errorf("new superspine downlink on %s, want eth7", l.Spec.EndpointA.Interface)
	}
}

// TestScaleOutAddServer bumps pod-1's servers per rack: each pod-1
// rack gains one server on the next host address and each leaf grows
// a fresh access port.
func TestScaleOutAddServer(t *testing.T) {
	spec := standardSpec()
	baseRes := rebuild(t, spec, nil)
	base := &Base{Nodes: baseRes.Nodes, Links: baseRes.Links}

	spec.Topology.Pods[0].ServersPerRack = 3
	res := rebuild(t, spec, base)

	assertSurvivorsUnchanged(t, base, res)

	srv := nodeByName(res, "pod-1-rack-1-server-3")
	if srv == nil {
		t.Fatal("missing new server")
	}

	if got := srv.Spec.Address.String(); got != "10.100.0.13/24" {
		t.Errorf("new server address: got %s, want 10.100.0.13/24", got)
	}

	if nodeByName(res, "pod-2-rack-3-server-3") != nil {
		t.Error("pod-2 rack gained a server, scaling must stay per pod")
	}
}

// TestScaleInTailRack shrinks pod-2 to one rack: rack-4 disappears and
// every survivor keeps its identity.
func TestScaleInTailRack(t *testing.T) {
	spec := standardSpec()
	baseRes := rebuild(t, spec, nil)
	base := &Base{Nodes: baseRes.Nodes, Links: baseRes.Links}

	spec.Topology.Pods[1].Racks = 1
	res := rebuild(t, spec, base)

	assertSurvivorsUnchanged(t, base, res)

	if nodeByName(res, "pod-2-rack-4-leaf-a") != nil {
		t.Error("rack-4 still present after scale-in")
	}

	if nodeByName(res, "pod-2-rack-3-leaf-a") == nil {
		t.Error("rack-3 removed; scale-in must drop the tail slot only")
	}
}

// TestScaleOutAfterScaleIn removes pod-1's second rack (applied), then
// adds a rack again. The rack number must stay unique against every
// rack still deployed (pod-2 holds rack-4, so the newcomer takes 5),
// while the spine interfaces rack-2 freed are reused: the base always
// reflects the interfaces that still exist in live containers, and an
// applied scale-in deleted rack-2's veths from the spines — like a
// switch port whose cable was pulled, the slot is genuinely free.
func TestScaleOutAfterScaleIn(t *testing.T) {
	spec := standardSpec()
	baseRes := rebuild(t, spec, nil)

	spec.Topology.Pods[0].Racks = 1
	shrunk := rebuild(t, spec, &Base{Nodes: baseRes.Nodes, Links: baseRes.Links})

	spec.Topology.Pods[0].Racks = 2
	res := rebuild(t, spec, &Base{Nodes: shrunk.Nodes, Links: shrunk.Links})

	if nodeByName(res, "pod-1-rack-2-leaf-a") != nil {
		t.Error("freed rack number 2 was reused")
	}

	leaf := nodeByName(res, "pod-1-rack-5-leaf-a")
	if leaf == nil {
		t.Fatalf("re-added rack not numbered 5: %v", nodeNames(res, model.RoleLeaf))
	}

	l := linkByName(res, "pod-1-spine-1--pod-1-rack-5-leaf-a")
	if l == nil {
		t.Fatal("missing spine link to re-added rack")
	}

	if l.Spec.EndpointA.Interface != "eth5" {
		t.Errorf("re-added rack downlink on %s, want the freed eth5", l.Spec.EndpointA.Interface)
	}
}

// TestRebuildWithoutChangesIsIdentical rebuilds the same spec over its
// own base: every node and link must survive with identical values and
// nothing new may appear.
func TestRebuildWithoutChangesIsIdentical(t *testing.T) {
	spec := standardSpec()
	baseRes := rebuild(t, spec, nil)
	base := &Base{Nodes: baseRes.Nodes, Links: baseRes.Links}

	res := rebuild(t, spec, base)

	assertSurvivorsUnchanged(t, base, res)
	if len(res.Nodes) != len(base.Nodes) || len(res.Links) != len(base.Links) {
		t.Errorf("no-change rebuild resized topology: %d/%d nodes, %d/%d links",
			len(res.Nodes), len(base.Nodes), len(res.Links), len(base.Links))
	}
}

func nodeNames(res *Result, role model.NodeRole) []string {
	var names []string
	for _, n := range res.Nodes {
		if n.Spec.Role == role {
			names = append(names, n.Meta.Name)
		}
	}

	return names
}
