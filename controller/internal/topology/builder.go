// Package topology builds the desired node and link set for a lab
// from its profile. All addresses and ASNs are taken from allocators;
// nothing here computes resources by formula.
package topology

import (
	"fmt"
	"net/netip"

	"github.com/ifantsai/dcnetlab/controller/internal/allocator/asn"
	"github.com/ifantsai/dcnetlab/controller/internal/allocator/ipam"
	"github.com/ifantsai/dcnetlab/internal/model"
)

// Result is the fully-allocated desired topology of one lab.
type Result struct {
	Nodes       []*model.Node
	Links       []*model.Link
	Allocations []model.Allocation
}

// Builder assembles nodes and links for a lab spec.
type Builder struct {
	labID      string
	pools      map[model.AddressPoolName]*ipam.Pool
	asns       *asn.Allocator
	ifIndex    map[string]int // node name -> next interface index
	rackNum    int            // global rack counter (drives the rack ASN)
	result     Result
	nodeByName map[string]*model.Node
}

// NewBuilder creates a builder with allocators initialised from spec.
func NewBuilder(labID string, spec model.LabSpec) (*Builder, error) {
	pools := make(map[model.AddressPoolName]*ipam.Pool)
	for _, p := range spec.Pools {
		pool, err := ipam.NewPool(string(p.Name), p.CIDR, p.AllocationPrefix)
		if err != nil {
			return nil, fmt.Errorf("create pool %s: %w", p.Name, err)
		}

		pools[p.Name] = pool
	}

	asns, err := asn.New(spec.ASNs)
	if err != nil {
		return nil, fmt.Errorf("create asn allocator: %w", err)
	}

	return &Builder{
		labID:      labID,
		pools:      pools,
		asns:       asns,
		ifIndex:    make(map[string]int),
		nodeByName: make(map[string]*model.Node),
	}, nil
}

// Build produces the full topology for spec.
func (b *Builder) Build(spec model.TopologySpec) (*Result, error) {
	var externals, edges, superspines []*model.Node

	for i := 1; i <= spec.ExternalRouters; i++ {
		n, err := b.addRouter(fmt.Sprintf("external-%d", i), model.RoleExternal, "")
		if err != nil {
			return nil, err
		}

		externals = append(externals, n)
	}

	for i := 1; i <= spec.DCEdges; i++ {
		n, err := b.addRouter(fmt.Sprintf("dcedge-%d", i), model.RoleDCEdge, "")
		if err != nil {
			return nil, err
		}

		edges = append(edges, n)
	}

	for i := 1; i <= spec.SuperSpines; i++ {
		n, err := b.addRouter(fmt.Sprintf("superspine-%d", i), model.RoleSuperSpine, "")
		if err != nil {
			return nil, err
		}

		superspines = append(superspines, n)
	}

	// External <-> DC Edge, DC Edge <-> SuperSpine: full mesh between tiers.
	for _, ext := range externals {
		for _, e := range edges {
			if err := b.addLink(ext, e); err != nil {
				return nil, err
			}
		}
	}

	for _, e := range edges {
		for _, ss := range superspines {
			if err := b.addLink(e, ss); err != nil {
				return nil, err
			}
		}
	}

	for pi, pod := range spec.Pods {
		podID := pod.Name
		if podID == "" {
			podID = fmt.Sprintf("pod-%d", pi+1)
		}

		var spines []*model.Node
		for i := 1; i <= pod.Spines; i++ {
			n, err := b.addRouter(fmt.Sprintf("%s-spine-%d", podID, i), model.RoleSpine, podID)
			if err != nil {
				return nil, err
			}

			spines = append(spines, n)
		}

		// SuperSpine <-> Spine.
		for _, ss := range superspines {
			for _, sp := range spines {
				if err := b.addLink(ss, sp); err != nil {
					return nil, err
				}
			}
		}

		// Racks: one MLAG leaf pair plus servers per rack.
		for r := 1; r <= pod.Racks; r++ {
			if err := b.addRack(podID, pod.ServersPerRack, spines); err != nil {
				return nil, err
			}
		}
	}

	return &b.result, nil
}

// addRack builds one rack: two MLAG leaves acting as the active-active
// VRRP gateway of the rack VLAN, the peer link between them, and the
// servers, each dual-homed with one bond member per leaf.
func (b *Builder) addRack(podID string, servers int, spines []*model.Node) error {
	b.rackNum++
	rackID := fmt.Sprintf("rack-%d", b.rackNum)
	rackASN := LeafRackASNBase + uint32(b.rackNum)

	// One subnet per rack: .1 virtual gateway, .2/.3 leaf vlanifs,
	// .11+ servers.
	sub, err := b.pools[model.PoolServerVlan].Allocate(podID + "/" + rackID)
	if err != nil {
		return fmt.Errorf("allocate rack subnet: %w", err)
	}

	bits := sub.Bits()
	gw := hostAt(sub, 1)
	vrid := (b.rackNum-1)%255 + 1
	gwMAC := fmt.Sprintf("00:00:5e:00:01:%02x", vrid) // standard VRRP virtual MAC

	leafA, err := b.addLeaf(fmt.Sprintf("%s-%s-leaf-a", podID, rackID), podID, rackID,
		rackASN, netip.PrefixFrom(hostAt(sub, 2), bits), gw, gwMAC, vrid, 200)
	if err != nil {
		return err
	}

	leafB, err := b.addLeaf(fmt.Sprintf("%s-%s-leaf-b", podID, rackID), podID, rackID,
		rackASN, netip.PrefixFrom(hostAt(sub, 3), bits), gw, gwMAC, vrid, 100)
	if err != nil {
		return err
	}

	leafA.Spec.MLAGPeer = leafB.Meta.Name
	leafB.Spec.MLAGPeer = leafA.Meta.Name

	b.result.Allocations = append(b.result.Allocations,
		model.Allocation{Pool: "asn:" + string(model.RoleLeaf), Value: fmt.Sprint(rackASN), Owner: podID + "/" + rackID},
		model.Allocation{Pool: string(model.PoolServerVlan), Value: sub.String(), Owner: podID + "/" + rackID},
	)

	// Spine <-> Leaf full mesh inside the pod.
	for _, sp := range spines {
		if err := b.addLink(sp, leafA); err != nil {
			return err
		}

		if err := b.addLink(sp, leafB); err != nil {
			return err
		}
	}

	// MLAG peer link trunks the server VLAN between the two leaves.
	b.addL2Link(leafA, leafB, model.LinkMLAGPeer)

	for si := 1; si <= servers; si++ {
		srv := b.addServer(fmt.Sprintf("%s-%s-server-%d", podID, rackID, si), podID, rackID,
			netip.PrefixFrom(hostAt(sub, 10+si), bits), gw,
			[]netip.Addr{leafA.Spec.VlanIP.Addr(), leafB.Spec.VlanIP.Addr()})
		// One bond member to each leaf of the pair.
		b.addL2Link(leafA, srv, model.LinkServerAccess)
		b.addL2Link(leafB, srv, model.LinkServerAccess)
	}

	return nil
}

// hostAt returns the nth host address inside a subnet.
func hostAt(p netip.Prefix, n int) netip.Addr {
	a := p.Masked().Addr()
	for i := 0; i < n; i++ {
		a = a.Next()
	}

	return a
}

func (b *Builder) addRouter(name string, role model.NodeRole, podID string) (*model.Node, error) {
	asnVal, err := b.asns.Allocate(role, name)
	if err != nil {
		return nil, fmt.Errorf("allocate asn for %s: %w", name, err)
	}

	loopback, err := b.pools[model.PoolLoopback].Allocate(name)
	if err != nil {
		return nil, fmt.Errorf("allocate loopback for %s: %w", name, err)
	}

	n := &model.Node{
		Meta: model.ResourceMeta{ID: model.NewID("node"), Name: name, Phase: model.PhasePending},
		Spec: model.NodeSpec{
			LabID:       b.labID,
			Role:        role,
			PodID:       podID,
			ASN:         asnVal,
			Loopback:    loopback,
			RuntimeType: model.RuntimeFRR,
		},
	}

	b.result.Nodes = append(b.result.Nodes, n)
	b.nodeByName[name] = n
	b.result.Allocations = append(b.result.Allocations,
		model.Allocation{Pool: "asn:" + string(role), Value: fmt.Sprint(asnVal), Owner: name},
		model.Allocation{Pool: string(model.PoolLoopback), Value: loopback.String(), Owner: name},
	)

	return n, nil
}

// addLeaf creates one member of a rack's MLAG pair. The ASN is the
// rack ASN (shared by both members); the vlanif carries the leaf's
// physical address plus the shared VRRP virtual gateway.
func (b *Builder) addLeaf(name, podID, rackID string, rackASN uint32,
	vlanIP netip.Prefix, gw netip.Addr, gwMAC string, vrid, prio int) (*model.Node, error) {
	loopback, err := b.pools[model.PoolLoopback].Allocate(name)
	if err != nil {
		return nil, fmt.Errorf("allocate loopback for %s: %w", name, err)
	}

	n := &model.Node{
		Meta: model.ResourceMeta{ID: model.NewID("node"), Name: name, Phase: model.PhasePending},
		Spec: model.NodeSpec{
			LabID:        b.labID,
			Role:         model.RoleLeaf,
			PodID:        podID,
			RackID:       rackID,
			ASN:          rackASN,
			Loopback:     loopback,
			RuntimeType:  model.RuntimeFRR,
			VlanID:       ServerVlanID,
			VlanIP:       vlanIP,
			GatewayIP:    gw,
			GatewayMAC:   gwMAC,
			VRRPGroup:    vrid,
			VRRPPriority: prio,
		},
	}

	b.result.Nodes = append(b.result.Nodes, n)
	b.nodeByName[name] = n
	b.result.Allocations = append(b.result.Allocations,
		model.Allocation{Pool: string(model.PoolLoopback), Value: loopback.String(), Owner: name})

	return n, nil
}

// addServer creates a dual-homed server: its bond0 lives on the rack
// VLAN and peers BGP with the physical vlanif addresses of both
// leaves (never the virtual gateway).
func (b *Builder) addServer(name, podID, rackID string, addr netip.Prefix,
	gw netip.Addr, bgpPeers []netip.Addr) *model.Node {
	n := &model.Node{
		Meta: model.ResourceMeta{ID: model.NewID("node"), Name: name, Phase: model.PhasePending},
		Spec: model.NodeSpec{
			LabID:          b.labID,
			Role:           model.RoleServer,
			PodID:          podID,
			RackID:         rackID,
			ASN:            ServerASN,
			RuntimeType:    model.RuntimeFRR,
			Address:        addr,
			DefaultGateway: gw,
			BGPPeers:       bgpPeers,
		},
	}

	b.result.Nodes = append(b.result.Nodes, n)
	b.nodeByName[name] = n

	return n
}

// addLink connects two routers with a /31 from the fabric P2P pool.
// Endpoint A (the upper tier) takes the first address.
func (b *Builder) addLink(a, z *model.Node) error {
	linkName := a.Meta.Name + "--" + z.Meta.Name

	sub, err := b.pools[model.PoolFabricP2P].Allocate(linkName)
	if err != nil {
		return fmt.Errorf("allocate p2p subnet for %s: %w", linkName, err)
	}

	first := sub.Masked().Addr()
	second := first.Next()

	l := &model.Link{
		Meta: model.ResourceMeta{ID: model.NewID("link"), Name: linkName, Phase: model.PhasePending},
		Spec: model.LinkSpec{
			LabID:     b.labID,
			Kind:      model.LinkFabric,
			EndpointA: b.endpoint(a, netip.PrefixFrom(first, sub.Bits())),
			EndpointB: b.endpoint(z, netip.PrefixFrom(second, sub.Bits())),
			MTU:       9100,
		},
	}

	b.result.Links = append(b.result.Links, l)
	b.result.Allocations = append(b.result.Allocations,
		model.Allocation{Pool: string(model.PoolFabricP2P), Value: sub.String(), Owner: linkName})

	return nil
}

// addL2Link connects two nodes at layer 2 on the server VLAN: either
// a leaf access port towards a server bond member or the MLAG peer
// link between the two leaves. No endpoint addresses.
func (b *Builder) addL2Link(a, z *model.Node, kind model.LinkKind) {
	linkName := a.Meta.Name + "--" + z.Meta.Name
	l := &model.Link{
		Meta: model.ResourceMeta{ID: model.NewID("link"), Name: linkName, Phase: model.PhasePending},
		Spec: model.LinkSpec{
			LabID:     b.labID,
			Kind:      kind,
			VlanID:    ServerVlanID,
			EndpointA: b.endpoint(a, netip.Prefix{}),
			EndpointB: b.endpoint(z, netip.Prefix{}),
			MTU:       9100,
		},
	}

	b.result.Links = append(b.result.Links, l)
}

func (b *Builder) endpoint(n *model.Node, addr netip.Prefix) model.LinkEndpoint {
	b.ifIndex[n.Meta.Name]++

	return model.LinkEndpoint{
		NodeID:    n.Meta.ID,
		NodeName:  n.Meta.Name,
		Interface: fmt.Sprintf("eth%d", b.ifIndex[n.Meta.Name]),
		Address:   addr,
	}
}
