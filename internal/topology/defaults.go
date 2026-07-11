package topology

import "github.com/ifantsai/dcnetlab/internal/model"

// Fixed ASN plan for the leaf/server layer. Leaves are numbered per
// rack (both MLAG members of a rack share the rack ASN) and every
// server uses the same well-known ASN, so neither goes through the
// range allocator.
const (
	// ServerASN is the ASN used by every server for BGP towards its
	// rack's leaf pair.
	ServerASN uint32 = 65000
	// LeafRackASNBase plus the global rack number gives the rack's
	// leaf ASN (rack-1 -> 4200080001).
	LeafRackASNBase uint32 = 4200080000
	// ServerVlanID is the VLAN on the leaves that carries the rack's
	// server segment; server-facing ports are access ports on it.
	ServerVlanID = 1000
)

// DefaultPools returns the address pools from the design document.
// Everything internal is carved out of 10.0.0.0/8: infrastructure
// pools take one /16 block each in the lower half (10.4–10.99 and
// 10.101–10.127 stay spare), the upper half 10.128.0.0/9 is reserved
// wholesale for overlay workloads.
func DefaultPools() []model.AddressPool {
	return []model.AddressPool{
		{Name: model.PoolFabricP2P, CIDR: "10.0.0.0/16", AllocationPrefix: 31},
		{Name: model.PoolLoopback, CIDR: "10.1.0.0/16", AllocationPrefix: 32},
		{Name: model.PoolVTEP, CIDR: "10.2.0.0/16", AllocationPrefix: 32},
		{Name: model.PoolManagement, CIDR: "10.3.0.0/16", AllocationPrefix: 32},
		// One /24 per rack (10.100.<rack>.0/24): .1 = VRRP virtual
		// gateway, .2/.3 = leaf vlanif physical addresses, .11+ = servers.
		{Name: model.PoolServerVlan, CIDR: "10.100.0.0/16", AllocationPrefix: 24},
		{Name: model.PoolWorkload, CIDR: "10.128.0.0/9", AllocationPrefix: 24},
		// Simulated Internet space stays outside 10/8 on purpose
		// (RFC 2544 benchmark range).
		{Name: model.PoolPublic, CIDR: "198.18.0.0/15", AllocationPrefix: 32},
	}
}

// DefaultASNRanges returns the per-role ASN plan. Leaf and server
// ASNs are computed (see LeafRackASNBase/ServerASN), not allocated
// from a range. DC edge starts at 64600 so it cannot collide with the
// well-known server ASN 65000.
func DefaultASNRanges() []model.ASNRange {
	return []model.ASNRange{
		{Role: model.RoleExternal, Start: 64500, End: 64599},
		{Role: model.RoleDCEdge, Start: 64600, End: 64699},
		{Role: model.RoleSuperSpine, Start: 65100, End: 65199},
		{Role: model.RoleSpine, Start: 65200, End: 65999},
	}
}

// Profiles returns the built-in topology profiles.
func Profiles() map[model.ProfileName]model.TopologySpec {
	return map[model.ProfileName]model.TopologySpec{
		model.ProfileMicro: {
			ExternalRouters: 1,
			DCEdges:         1,
			SuperSpines:     1,
			Pods: []model.PodSpec{
				{Name: "pod-1", Spines: 2, Racks: 1, ServersPerRack: 2},
			},
		},
		model.ProfileStandard: {
			ExternalRouters: 1,
			DCEdges:         2,
			SuperSpines:     2,
			Pods: []model.PodSpec{
				{Name: "pod-1", Spines: 2, Racks: 2, ServersPerRack: 2},
				{Name: "pod-2", Spines: 2, Racks: 2, ServersPerRack: 2},
			},
		},
	}
}
