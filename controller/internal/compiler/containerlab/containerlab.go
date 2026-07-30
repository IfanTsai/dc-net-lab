// Package containerlab compiles a Containerlab topology file from the
// desired topology. The generated YAML is a build artifact, never a
// source of truth.
package containerlab

import (
	"fmt"
	"sort"

	"gopkg.in/yaml.v3"

	"github.com/ifantsai/dcnetlab/internal/model"
	"github.com/ifantsai/dcnetlab/internal/nodeagentapi"
)

// Options control image selection for generated nodes.
type Options struct {
	FRRImage    string
	ServerImage string
	// EdgeImage is the FRR + iptables image dcedge and external nodes
	// run when InternetAccess is set: both need NAT rules the plain
	// FRR image cannot install. Built locally via `make edge-image`.
	EdgeImage string
	// InternetAccess connects the fabric to the real internet:
	// dc-edges SNAT non-fabric traffic to their loopback at the DC
	// boundary and externals originate a default route (the WAN
	// attachment itself happens post-deploy, outside the compiler).
	InternetAccess bool
}

// DefaultOptions returns the default runtime images, all built by
// `make images`: dcnetlab/frr is the official FRR plus the baked-in
// capture tool (switches and routers are the primary capture
// targets), dcnetlab/server adds node-agent and node-cli on top of
// FRR — servers speak BGP to their rack's leaf pair. The server's
// capture tool is not baked in: the controller delivers it through
// the package repository during apply.
func DefaultOptions() Options {
	return Options{
		FRRImage:    "dcnetlab/frr:10.2.1",
		ServerImage: "dcnetlab/server:10.2.1",
		EdgeImage:   "dcnetlab/frr-edge:10.2.1",
	}
}

// startNodeAgent launches node-agent in the background at deploy
// time. The script itself is shared with the observer's agent
// self-heal so both boot paths stay identical.
const startNodeAgent = `sh -c '` + nodeagentapi.StartAgentScript + `'`

// removeMgmtDefaultRoute drops the default route Containerlab injects
// via the management network (eth0) so exec-time provisioning (e.g.
// package installs) can reach the internet. Left in place, it becomes
// a silent fallback path once a device's real DC-facing route is
// genuinely gone — e.g. BGP withdraws a prefix because the device
// providing it was paused to simulate a power-off — so traffic that
// should time out instead leaks out the management network. Removing
// it makes "unreachable" mean unreachable. Servers do not need this:
// their default route is set explicitly via ip route replace.
const removeMgmtDefaultRoute = "ip route del default dev eth0"

// Topology mirrors the subset of the Containerlab schema we emit.
type Topology struct {
	Name     string       `yaml:"name"`
	Topology TopologyBody `yaml:"topology"`
}

type TopologyBody struct {
	Nodes map[string]NodeDef `yaml:"nodes"`
	Links []LinkDef          `yaml:"links"`
}

type NodeDef struct {
	Kind  string   `yaml:"kind"`
	Image string   `yaml:"image"`
	Binds []string `yaml:"binds,omitempty"`
	Exec  []string `yaml:"exec,omitempty"`
}

type LinkDef struct {
	Endpoints []string `yaml:"endpoints"`
}

// Compile renders the Containerlab topology YAML for a lab. All FRR
// nodes (routers and servers) bind their generated configs from the
// configs/ directory next to the topology file. Leaves additionally
// build their VLAN-filtering bridge, vlanif and VRRP macvlan via
// exec; servers assemble bond0 from their two access links.
func Compile(labName string, nodes []*model.Node, links []*model.Link, opts Options) ([]byte, error) {
	defs := make(map[string]NodeDef, len(nodes))

	// Per-node L2 port roles derived from the links.
	accessPorts := make(map[string][]string) // leaf: untagged server-facing ports
	trunkPorts := make(map[string][]string)  // leaf: MLAG peer-link ports
	bondMembers := make(map[string][]string) // server: bond member ports
	for _, l := range links {
		a, z := l.Spec.EndpointA, l.Spec.EndpointB
		switch l.Spec.Kind {
		case model.LinkServerAccess:
			accessPorts[a.NodeID] = append(accessPorts[a.NodeID], a.Interface)
			bondMembers[z.NodeID] = append(bondMembers[z.NodeID], z.Interface)
		case model.LinkMLAGPeer:
			trunkPorts[a.NodeID] = append(trunkPorts[a.NodeID], a.Interface)
			trunkPorts[z.NodeID] = append(trunkPorts[z.NodeID], z.Interface)
		}
	}

	for _, n := range nodes {
		if !n.Spec.IsRouter() {
			return nil, fmt.Errorf("node %s: unsupported runtime %q", n.Meta.Name, n.Spec.RuntimeType)
		}

		def := NodeDef{
			Kind:  "linux",
			Image: opts.FRRImage,
			Binds: []string{
				fmt.Sprintf("configs/%s/daemons:/etc/frr/daemons", n.Meta.Name),
				fmt.Sprintf("configs/%s/frr.conf:/etc/frr/frr.conf", n.Meta.Name),
				fmt.Sprintf("configs/%s/vtysh.conf:/etc/frr/vtysh.conf", n.Meta.Name),
			},
		}

		switch {
		case n.Spec.Role == model.RoleServer:
			def.Image = opts.ServerImage
			def.Exec = append(serverExec(n, bondMembers[n.Meta.ID]), startNodeAgent)
		case n.Spec.VlanID != 0:
			def.Exec = leafExec(n, accessPorts[n.Meta.ID], trunkPorts[n.Meta.ID])
		case n.Spec.Role == model.RoleDCEdge && opts.InternetAccess:
			def.Image = opts.EdgeImage
			def.Exec = dcedgeExec(n)
		case n.Spec.Role == model.RoleExternal && opts.InternetAccess:
			// The WAN attachment, default route and masquerade are
			// injected post-deploy (the WAN interface does not exist
			// yet); only the image must already ship iptables.
			def.Image = opts.EdgeImage
			def.Exec = []string{removeMgmtDefaultRoute}
		default:
			// external / dc-edge / superspine / spine: pure L3 routers
			// with no other exec-time setup, but still need the
			// management default route removed.
			def.Exec = []string{removeMgmtDefaultRoute}
		}

		defs[n.Meta.Name] = def
	}

	linkDefs := make([]LinkDef, 0, len(links))
	for _, l := range links {
		linkDefs = append(linkDefs, LinkDef{Endpoints: []string{
			fmt.Sprintf("%s:%s", l.Spec.EndpointA.NodeName, l.Spec.EndpointA.Interface),
			fmt.Sprintf("%s:%s", l.Spec.EndpointB.NodeName, l.Spec.EndpointB.Interface),
		}})
	}

	sort.Slice(linkDefs, func(i, j int) bool {
		return linkDefs[i].Endpoints[0] < linkDefs[j].Endpoints[0] ||
			(linkDefs[i].Endpoints[0] == linkDefs[j].Endpoints[0] &&
				linkDefs[i].Endpoints[1] < linkDefs[j].Endpoints[1])
	})

	return yaml.Marshal(Topology{
		Name: labName,
		Topology: TopologyBody{
			Nodes: defs,
			Links: linkDefs,
		},
	})
}

// leafExec builds the leaf's L2 plumbing: a VLAN-filtering bridge
// with untagged server access ports and the tagged MLAG peer link, a
// vlanif for the gateway VLAN (addressed by zebra from frr.conf) and
// the macvlan that FRR's vrrpd manages for the virtual gateway
// address and MAC.
func leafExec(n *model.Node, access, trunk []string) []string {
	vid := n.Spec.VlanID
	cmds := []string{
		removeMgmtDefaultRoute,
		"ip link add br0 type bridge",
		"ip link set br0 type bridge vlan_filtering 1",
		"ip link set br0 up",
	}

	for _, p := range access {
		cmds = append(cmds,
			fmt.Sprintf("ip link set %s master br0", p),
			fmt.Sprintf("bridge vlan add dev %s vid %d pvid untagged", p, vid),
		)
	}

	for _, p := range trunk {
		cmds = append(cmds,
			fmt.Sprintf("ip link set %s master br0", p),
			fmt.Sprintf("bridge vlan add dev %s vid %d", p, vid),
		)
	}

	cmds = append(cmds,
		fmt.Sprintf("bridge vlan add dev br0 vid %d self", vid),
		fmt.Sprintf("ip link add link br0 name vlan%d type vlan id %d", vid, vid),
		fmt.Sprintf("ip link set vlan%d up", vid),
	)
	if n.Spec.VRRPGroup != 0 && n.Spec.GatewayIP.IsValid() {
		mv := fmt.Sprintf("vrrp4-%d-1", n.Spec.VRRPGroup)
		cmds = append(cmds,
			// Per the FRR vrrpd docs: macvlan with the virtual MAC,
			// the VIP as /32 and admin up (vrrpd drives protodown for
			// master/backup). No addrgenmode random: it needs IPv6
			// support the WSL2 kernel lacks, and VRRP here is v4 only.
			fmt.Sprintf("ip link add %s link vlan%d type macvlan mode bridge", mv, vid),
			fmt.Sprintf("ip link set dev %s address %s", mv, n.Spec.GatewayMAC),
			fmt.Sprintf("ip addr add %s/32 dev %s", n.Spec.GatewayIP, mv),
			fmt.Sprintf("ip link set dev %s up", mv),
		)
	}

	return cmds
}

// fabricSupernet mirrors topology.DefaultPools: every simulated
// address (fabric links, loopbacks, server VLANs, workloads) is
// carved out of this block, so a destination outside it is
// internet-bound.
const fabricSupernet = "10.0.0.0/8"

// dcedgeExec adds the DC-boundary NAT: internet-bound traffic is
// SNATed to the dcedge's loopback, so private fabric addresses never
// cross the DC boundary and, because each loopback is unique and
// BGP-reachable, return traffic is anchored to the dcedge holding the
// conntrack state even with ECMP across the edge pair. Fabric-internal
// destinations (future DCI traffic included) and the management
// network (eth0) stay untranslated.
func dcedgeExec(n *model.Node) []string {
	return []string{
		removeMgmtDefaultRoute,
		fmt.Sprintf("iptables -t nat -A POSTROUTING ! -d %s ! -o eth0 -j SNAT --to-source %s",
			fabricSupernet, n.Spec.Loopback.Addr()),
	}
}

// serverExec assembles the server's bond0 from its two access-link
// members (active-backup: no MLAG control plane exists between the
// leaves) and points the default route at the rack's virtual gateway.
// bond0's address comes from frr.conf via zebra.
func serverExec(n *model.Node, members []string) []string {
	cmds := []string{
		"ip link add bond0 type bond mode active-backup miimon 100",
	}

	for _, p := range members {
		cmds = append(cmds,
			fmt.Sprintf("ip link set %s down", p),
			fmt.Sprintf("ip link set %s master bond0", p),
		)
	}

	cmds = append(cmds, "ip link set bond0 up")
	if n.Spec.DefaultGateway.IsValid() {
		// onlink: at exec time zebra may not have addressed bond0 yet,
		// so the nexthop is not resolvable and a plain route replace
		// would race FRR startup and occasionally fail, leaving the
		// management network as the silent default.
		cmds = append(cmds, fmt.Sprintf("ip route replace default via %s dev bond0 onlink", n.Spec.DefaultGateway))
	}

	return cmds
}
