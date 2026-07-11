package topology

import (
	"testing"

	"github.com/ifantsai/dcnetlab/internal/model"
)

func buildProfile(t *testing.T, profile model.ProfileName) *Result {
	t.Helper()
	spec := model.LabSpec{
		Profile:  profile,
		Topology: Profiles()[profile],
		Pools:    DefaultPools(),
		ASNs:     DefaultASNRanges(),
	}

	b, err := NewBuilder("lab-test", spec)
	if err != nil {
		t.Fatal(err)
	}

	res, err := b.Build(spec.Topology)
	if err != nil {
		t.Fatal(err)
	}

	return res
}

func countByRole(res *Result) map[model.NodeRole]int {
	c := make(map[model.NodeRole]int)
	for _, n := range res.Nodes {
		c[n.Spec.Role]++
	}

	return c
}

func TestMicroProfile(t *testing.T) {
	res := buildProfile(t, model.ProfileMicro)
	c := countByRole(res)
	want := map[model.NodeRole]int{
		model.RoleExternal:   1,
		model.RoleDCEdge:     1,
		model.RoleSuperSpine: 1,
		model.RoleSpine:      2,
		model.RoleLeaf:       2, // one rack = one MLAG pair
		model.RoleServer:     2,
	}

	for role, n := range want {
		if c[role] != n {
			t.Errorf("role %s: got %d, want %d", role, c[role], n)
		}
	}

	// ext-edge(1) + edge-ss(1) + ss-spine(2) + spine-leaf(4) +
	// mlag-peer(1) + server-access(2 servers x 2 leaves = 4)
	if len(res.Links) != 13 {
		t.Errorf("links: got %d, want 13", len(res.Links))
	}
}

func TestStandardProfile(t *testing.T) {
	res := buildProfile(t, model.ProfileStandard)
	c := countByRole(res)
	if c[model.RoleSpine] != 4 || c[model.RoleLeaf] != 8 || c[model.RoleServer] != 8 {
		t.Errorf("got spine=%d leaf=%d server=%d", c[model.RoleSpine], c[model.RoleLeaf], c[model.RoleServer])
	}
}

func TestUniqueAllocations(t *testing.T) {
	res := buildProfile(t, model.ProfileStandard)

	// ASNs are unique per device except: the two MLAG leaves of a rack
	// share the rack ASN, and every server uses ServerASN.
	asnOwner := make(map[uint32]string) // asn -> rack (leaf) or name
	loopbacks := make(map[string]string)
	for _, n := range res.Nodes {
		if !n.Spec.IsRouter() || n.Spec.Role == model.RoleServer {
			continue
		}

		owner := n.Meta.Name
		if n.Spec.Role == model.RoleLeaf {
			owner = n.Spec.RackID
			if want := LeafRackASNBase; n.Spec.ASN <= want || n.Spec.ASN > want+1000 {
				t.Errorf("%s: leaf asn %d not rack-based", n.Meta.Name, n.Spec.ASN)
			}
		}

		if prev, dup := asnOwner[n.Spec.ASN]; dup && prev != owner {
			t.Errorf("asn %d used by %s and %s", n.Spec.ASN, prev, owner)
		}

		asnOwner[n.Spec.ASN] = owner

		lb := n.Spec.Loopback.String()
		if prev, dup := loopbacks[lb]; dup {
			t.Errorf("loopback %s used by %s and %s", lb, prev, n.Meta.Name)
		}

		loopbacks[lb] = n.Meta.Name
	}

	addrs := make(map[string]string)
	for _, l := range res.Links {
		for _, ep := range []model.LinkEndpoint{l.Spec.EndpointA, l.Spec.EndpointB} {
			if !ep.Address.IsValid() {
				continue // L2 link
			}

			key := ep.Address.Addr().String()
			if prev, dup := addrs[key]; dup {
				t.Errorf("address %s used by %s and %s", key, prev, l.Meta.Name)
			}

			addrs[key] = l.Meta.Name
		}
	}
}

func TestInterfaceNamesUniquePerNode(t *testing.T) {
	res := buildProfile(t, model.ProfileStandard)
	seen := make(map[string]bool)
	for _, l := range res.Links {
		for _, ep := range []model.LinkEndpoint{l.Spec.EndpointA, l.Spec.EndpointB} {
			key := ep.NodeName + "/" + ep.Interface
			if seen[key] {
				t.Errorf("duplicate interface %s", key)
			}

			seen[key] = true
		}
	}
}

// TestRackWiring checks the MLAG rack invariants: every server is
// dual-homed to both leaves of its rack, all rack members share one
// VLAN subnet, and BGP peers are the leaves' physical vlanif
// addresses, not the virtual gateway.
func TestRackWiring(t *testing.T) {
	res := buildProfile(t, model.ProfileStandard)

	leavesByRack := make(map[string][]*model.Node)
	for _, n := range res.Nodes {
		if n.Spec.Role == model.RoleLeaf {
			leavesByRack[n.Spec.RackID] = append(leavesByRack[n.Spec.RackID], n)
		}
	}

	for rack, leaves := range leavesByRack {
		if len(leaves) != 2 {
			t.Fatalf("rack %s: %d leaves, want an MLAG pair", rack, len(leaves))
		}

		a, b := leaves[0], leaves[1]
		if a.Spec.MLAGPeer != b.Meta.Name || b.Spec.MLAGPeer != a.Meta.Name {
			t.Errorf("rack %s: MLAG peers not cross-referenced", rack)
		}

		if a.Spec.ASN != b.Spec.ASN {
			t.Errorf("rack %s: leaf ASNs differ: %d vs %d", rack, a.Spec.ASN, b.Spec.ASN)
		}

		if a.Spec.GatewayIP != b.Spec.GatewayIP || a.Spec.GatewayMAC != b.Spec.GatewayMAC {
			t.Errorf("rack %s: virtual gateway differs between the pair", rack)
		}

		if a.Spec.VlanIP.Addr() == b.Spec.VlanIP.Addr() {
			t.Errorf("rack %s: leaves share the physical vlanif address", rack)
		}

		if a.Spec.VlanIP.Masked() != b.Spec.VlanIP.Masked() {
			t.Errorf("rack %s: leaves on different VLAN subnets", rack)
		}

		if a.Spec.VRRPPriority == b.Spec.VRRPPriority {
			t.Errorf("rack %s: equal VRRP priorities", rack)
		}
	}

	accessCount := make(map[string]int) // server id -> access links
	byID := make(map[string]*model.Node)
	for _, n := range res.Nodes {
		byID[n.Meta.ID] = n
	}

	for _, l := range res.Links {
		if l.Spec.Kind == model.LinkServerAccess {
			accessCount[l.Spec.EndpointB.NodeID]++
			if l.Spec.VlanID != ServerVlanID {
				t.Errorf("link %s: vlan %d, want %d", l.Meta.Name, l.Spec.VlanID, ServerVlanID)
			}
		}
	}

	for _, n := range res.Nodes {
		if n.Spec.Role != model.RoleServer {
			continue
		}

		if accessCount[n.Meta.ID] != 2 {
			t.Errorf("server %s: %d access links, want 2", n.Meta.Name, accessCount[n.Meta.ID])
		}

		if n.Spec.ASN != ServerASN {
			t.Errorf("server %s: asn %d, want %d", n.Meta.Name, n.Spec.ASN, ServerASN)
		}

		if !n.Spec.DefaultGateway.IsValid() {
			t.Errorf("server %s has no default gateway", n.Meta.Name)
		}

		if len(n.Spec.BGPPeers) != 2 {
			t.Fatalf("server %s: %d bgp peers, want 2", n.Meta.Name, len(n.Spec.BGPPeers))
		}

		leaves := leavesByRack[n.Spec.RackID]
		want := map[string]bool{
			leaves[0].Spec.VlanIP.Addr().String(): true,
			leaves[1].Spec.VlanIP.Addr().String(): true,
		}

		for _, p := range n.Spec.BGPPeers {
			if !want[p.String()] {
				t.Errorf("server %s: bgp peer %s is not a leaf physical vlanif", n.Meta.Name, p)
			}

			if p == n.Spec.DefaultGateway {
				t.Errorf("server %s: bgp peer %s is the virtual gateway", n.Meta.Name, p)
			}
		}
	}
}

func TestASNsInRoleRange(t *testing.T) {
	res := buildProfile(t, model.ProfileStandard)
	ranges := make(map[model.NodeRole]model.ASNRange)
	for _, r := range DefaultASNRanges() {
		ranges[r.Role] = r
	}

	for _, n := range res.Nodes {
		if !n.Spec.IsRouter() {
			continue
		}

		switch n.Spec.Role {
		case model.RoleLeaf, model.RoleServer:
			continue // computed, checked elsewhere
		}

		r := ranges[n.Spec.Role]
		if n.Spec.ASN < r.Start || n.Spec.ASN > r.End {
			t.Errorf("%s: asn %d outside %d-%d", n.Meta.Name, n.Spec.ASN, r.Start, r.End)
		}
	}
}
