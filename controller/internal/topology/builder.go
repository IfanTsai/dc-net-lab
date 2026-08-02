// Package topology builds the desired node and link set for a lab
// from its profile. All addresses and ASNs are taken from allocators;
// nothing here computes resources by formula.
package topology

import (
	"fmt"
	"net/netip"
	"sort"

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

// Base is the deployed topology a rebuild must stay consistent with.
// It pins everything that is invisible in the spec but very visible in
// the running containers: which global rack number a pod slot owns,
// which eth index a link landed on, and which address or ASN an owner
// already holds. Without it, scaling a lab would renumber and
// re-address nodes that are alive and converged.
type Base struct {
	Nodes []*model.Node
	Links []*model.Link
}

// Builder assembles nodes and links for a lab spec.
type Builder struct {
	labID      string
	pools      map[model.AddressPoolName]*ipam.Pool
	asns       *asn.Allocator
	ifIndex    map[string]int   // node name -> next interface index
	maxRack    int              // highest global rack number ever assigned
	rackSlots  map[string][]int // pod -> existing global rack numbers, ascending
	baseLinks  map[string]*model.Link
	baseOwners map[string]bool                                   // "pool|owner" held in the base
	pinnedSub  map[model.AddressPoolName]map[string]netip.Prefix // pool -> owner -> subnet
	pinnedASN  map[model.NodeRole]map[string]uint32
	result     Result
	nodeByName map[string]*model.Node
}

// NewBuilder creates a builder with allocators initialised from spec.
// A non-nil base restores the allocators and identity maps from the
// deployed topology so unchanged nodes and links rebuild bit-for-bit.
func NewBuilder(labID string, spec model.LabSpec, base *Base) (*Builder, error) {
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

	b := &Builder{
		labID:      labID,
		pools:      pools,
		asns:       asns,
		ifIndex:    make(map[string]int),
		rackSlots:  make(map[string][]int),
		baseLinks:  make(map[string]*model.Link),
		baseOwners: make(map[string]bool),
		pinnedSub:  make(map[model.AddressPoolName]map[string]netip.Prefix),
		pinnedASN:  make(map[model.NodeRole]map[string]uint32),
		nodeByName: make(map[string]*model.Node),
	}

	if base != nil {
		if err := b.restore(base); err != nil {
			return nil, err
		}
	}

	return b, nil
}

// restore replays the deployed topology into the allocators and
// identity maps. Owners keep their subnets and ASNs, pod slots keep
// their global rack numbers, links keep their eth indices, and the
// per-node interface counters resume past the highest index still
// deployed — new links never collide with an interface that exists in
// a live container, while indices an applied scale-in freed become
// reusable (the veths are genuinely gone from the kernel, like a
// switch port whose cable was pulled).
func (b *Builder) restore(base *Base) error {
	rackSeen := make(map[string]bool)
	for _, n := range base.Nodes {
		if n.Spec.Loopback.IsValid() {
			if err := b.pin(model.PoolLoopback, n.Meta.Name, n.Spec.Loopback); err != nil {
				return err
			}
		}

		switch n.Spec.Role {
		case model.RoleServer:
			// Fixed well-known ASN, addressed inside the rack subnet.
		case model.RoleLeaf:
			num, err := rackNumber(n.Spec.RackID)
			if err != nil {
				return fmt.Errorf("node %s: %w", n.Meta.Name, err)
			}

			owner := n.Spec.PodID + "/" + n.Spec.RackID
			if !rackSeen[owner] {
				rackSeen[owner] = true
				b.rackSlots[n.Spec.PodID] = append(b.rackSlots[n.Spec.PodID], num)
				if num > b.maxRack {
					b.maxRack = num
				}

				b.baseOwners["asn:"+string(model.RoleLeaf)+"|"+owner] = true
				if err := b.pin(model.PoolServerVlan, owner, n.Spec.VlanIP.Masked()); err != nil {
					return err
				}
			}
		default:
			if err := b.pinASN(n.Spec.Role, n.Meta.Name, n.Spec.ASN); err != nil {
				return err
			}
		}
	}

	for _, nums := range b.rackSlots {
		sort.Ints(nums)
	}

	for _, l := range base.Links {
		b.baseLinks[l.Meta.Name] = l
		if l.Spec.Kind == model.LinkFabric {
			if err := b.pin(model.PoolFabricP2P, l.Meta.Name, l.Spec.EndpointA.Address.Masked()); err != nil {
				return err
			}
		}

		for _, ep := range []model.LinkEndpoint{l.Spec.EndpointA, l.Spec.EndpointB} {
			var idx int
			if _, err := fmt.Sscanf(ep.Interface, "eth%d", &idx); err != nil {
				return fmt.Errorf("link %s: unrecognised interface %q", l.Meta.Name, ep.Interface)
			}

			if idx > b.ifIndex[ep.NodeName] {
				b.ifIndex[ep.NodeName] = idx
			}
		}
	}

	return nil
}

// pin records an owner's deployed subnet and marks it used in the pool.
func (b *Builder) pin(pool model.AddressPoolName, owner string, sub netip.Prefix) error {
	p, ok := b.pools[pool]
	if !ok {
		return fmt.Errorf("no pool %s to restore %s for %s", pool, sub, owner)
	}

	if err := p.Restore(sub, owner); err != nil {
		return fmt.Errorf("restore %s: %w", owner, err)
	}

	if b.pinnedSub[pool] == nil {
		b.pinnedSub[pool] = make(map[string]netip.Prefix)
	}

	b.pinnedSub[pool][owner] = sub
	b.baseOwners[string(pool)+"|"+owner] = true

	return nil
}

// IsNewAllocation reports whether an allocation of the rebuilt
// topology was not yet held in the base — the part of a plan preview
// that actually consumes resources.
func (b *Builder) IsNewAllocation(a model.Allocation) bool {
	return !b.baseOwners[a.Pool+"|"+a.Owner]
}

// pinASN records an owner's deployed ASN and marks it used.
func (b *Builder) pinASN(role model.NodeRole, owner string, val uint32) error {
	if err := b.asns.Restore(role, val, owner); err != nil {
		return fmt.Errorf("restore asn of %s: %w", owner, err)
	}

	if b.pinnedASN[role] == nil {
		b.pinnedASN[role] = make(map[string]uint32)
	}

	b.pinnedASN[role][owner] = val
	b.baseOwners["asn:"+string(role)+"|"+owner] = true

	return nil
}

// allocate returns the owner's pinned subnet if it had one in the base
// topology, or a fresh subnet from the pool.
func (b *Builder) allocate(pool model.AddressPoolName, owner string) (netip.Prefix, error) {
	if sub, ok := b.pinnedSub[pool][owner]; ok {
		return sub, nil
	}

	return b.pools[pool].Allocate(owner)
}

// allocateASN returns the owner's pinned ASN if it had one in the base
// topology, or a fresh ASN from the role range.
func (b *Builder) allocateASN(role model.NodeRole, owner string) (uint32, error) {
	if val, ok := b.pinnedASN[role][owner]; ok {
		return val, nil
	}

	return b.asns.Allocate(role, owner)
}

// rackNumForSlot maps a pod's ordinal rack slot to its global rack
// number: deployed slots keep the number they had, new slots take the
// next never-used one. Freed numbers are not reused within a rebuild;
// the slot lists reset from the base topology on the next plan.
func (b *Builder) rackNumForSlot(podID string, slot int) int {
	if nums := b.rackSlots[podID]; slot <= len(nums) {
		return nums[slot-1]
	}

	b.maxRack++

	return b.maxRack
}

// rackNumber parses the global number out of a "rack-N" id.
func rackNumber(rackID string) (int, error) {
	var num int
	if _, err := fmt.Sscanf(rackID, "rack-%d", &num); err != nil {
		return 0, fmt.Errorf("unrecognised rack id %q", rackID)
	}

	return num, nil
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

		// Racks: one MLAG leaf pair plus servers per rack. Slots are
		// ordinal within the pod; each slot keeps the global rack
		// number it was deployed with, and new slots take the next
		// never-used number so existing racks are never renumbered.
		for r := 1; r <= pod.Racks; r++ {
			if err := b.addRack(podID, r, pod.ServersPerRack, spines); err != nil {
				return nil, err
			}
		}
	}

	return &b.result, nil
}

// addRack builds one rack: two MLAG leaves acting as the active-active
// VRRP gateway of the rack VLAN, the peer link between them, and the
// servers, each dual-homed with one bond member per leaf.
func (b *Builder) addRack(podID string, slot, servers int, spines []*model.Node) error {
	rackNum := b.rackNumForSlot(podID, slot)
	rackID := fmt.Sprintf("rack-%d", rackNum)
	rackASN := LeafRackASNBase + uint32(rackNum)

	// One subnet per rack: .1 virtual gateway, .2/.3 leaf vlanifs,
	// .11+ servers.
	sub, err := b.allocate(model.PoolServerVlan, podID+"/"+rackID)
	if err != nil {
		return fmt.Errorf("allocate rack subnet: %w", err)
	}

	bits := sub.Bits()
	gw := hostAt(sub, 1)
	vrid := (rackNum-1)%255 + 1
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
	asnVal, err := b.allocateASN(role, name)
	if err != nil {
		return nil, fmt.Errorf("allocate asn for %s: %w", name, err)
	}

	loopback, err := b.allocate(model.PoolLoopback, name)
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
	loopback, err := b.allocate(model.PoolLoopback, name)
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

	sub, err := b.allocate(model.PoolFabricP2P, linkName)
	if err != nil {
		return fmt.Errorf("allocate p2p subnet for %s: %w", linkName, err)
	}

	first := sub.Masked().Addr()
	second := first.Next()
	ifA, ifB := b.pinnedInterfaces(linkName)

	l := &model.Link{
		Meta: model.ResourceMeta{ID: model.NewID("link"), Name: linkName, Phase: model.PhasePending},
		Spec: model.LinkSpec{
			LabID:     b.labID,
			Kind:      model.LinkFabric,
			EndpointA: b.endpoint(a, netip.PrefixFrom(first, sub.Bits()), ifA),
			EndpointB: b.endpoint(z, netip.PrefixFrom(second, sub.Bits()), ifB),
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
	ifA, ifB := b.pinnedInterfaces(linkName)
	l := &model.Link{
		Meta: model.ResourceMeta{ID: model.NewID("link"), Name: linkName, Phase: model.PhasePending},
		Spec: model.LinkSpec{
			LabID:     b.labID,
			Kind:      kind,
			VlanID:    ServerVlanID,
			EndpointA: b.endpoint(a, netip.Prefix{}, ifA),
			EndpointB: b.endpoint(z, netip.Prefix{}, ifB),
			MTU:       9100,
		},
	}

	b.result.Links = append(b.result.Links, l)
}

// pinnedInterfaces returns the eth names a deployed link's endpoints
// already occupy, or empty strings for a link the base does not have.
// The rebuild visits nodes and links in the original creation order,
// so endpoint orientation (A/B) is stable across generations.
func (b *Builder) pinnedInterfaces(linkName string) (string, string) {
	bl, ok := b.baseLinks[linkName]
	if !ok {
		return "", ""
	}

	return bl.Spec.EndpointA.Interface, bl.Spec.EndpointB.Interface
}

// endpoint attaches a link end to a node: on the interface the
// deployed link already occupies, or on the node's next free index.
func (b *Builder) endpoint(n *model.Node, addr netip.Prefix, pinnedIface string) model.LinkEndpoint {
	iface := pinnedIface
	if iface == "" {
		b.ifIndex[n.Meta.Name]++
		iface = fmt.Sprintf("eth%d", b.ifIndex[n.Meta.Name])
	}

	return model.LinkEndpoint{
		NodeID:    n.Meta.ID,
		NodeName:  n.Meta.Name,
		Interface: iface,
		Address:   addr,
	}
}
