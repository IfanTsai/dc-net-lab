package model

import (
	"net/netip"
	"slices"
	"time"
)

// NodeRole is the functional role of a device in the fabric.
type NodeRole string

const (
	RoleExternal   NodeRole = "external"
	RoleDCEdge     NodeRole = "dc-edge"
	RoleSuperSpine NodeRole = "superspine"
	RoleSpine      NodeRole = "spine"
	RoleLeaf       NodeRole = "leaf"
	RoleServer     NodeRole = "server"
)

// RuntimeType selects how a node is realised at runtime.
type RuntimeType string

const (
	RuntimeFRR   RuntimeType = "frr"   // FRR routing container
	RuntimeLinux RuntimeType = "linux" // plain Linux container (servers)
)

// RuntimeState is the observed runtime state of a node.
type RuntimeState string

const (
	RuntimeStateUnknown RuntimeState = "Unknown"
	RuntimeStateRunning RuntimeState = "Running"
	RuntimeStateStopped RuntimeState = "Stopped"
	RuntimeStateFailed  RuntimeState = "Failed"
)

// Node is one device in the fabric: router, switch or server.
type Node struct {
	Meta   ResourceMeta `json:"meta"`
	Spec   NodeSpec     `json:"spec"`
	Status NodeStatus   `json:"status"`
}

// NodeSpec is the desired state of a node.
type NodeSpec struct {
	LabID       string       `json:"labId"`
	Role        NodeRole     `json:"role"`
	PodID       string       `json:"podId,omitempty"`
	RackID      string       `json:"rackId,omitempty"`
	ASN         uint32       `json:"asn,omitempty"`
	Loopback    netip.Prefix `json:"loopback,omitempty"`
	MgmtIP      netip.Addr   `json:"mgmtIp,omitempty"`
	RuntimeType RuntimeType  `json:"runtimeType"`

	// Leaf-only: the two leaves of a rack form an MLAG pair and act as
	// the active-active gateway for the rack's server VLAN. Each leaf
	// has its own physical address on the VLAN interface (VlanIP) and
	// shares the VRRP virtual gateway address and MAC with its peer.
	// Servers that peer BGP with the leaves must target the physical
	// VlanIPs, never the virtual gateway address.
	MLAGPeer     string       `json:"mlagPeer,omitempty"`
	VlanID       int          `json:"vlanId,omitempty"`
	VlanIP       netip.Prefix `json:"vlanIp,omitempty"`
	GatewayIP    netip.Addr   `json:"gatewayIp,omitempty"`
	GatewayMAC   string       `json:"gatewayMac,omitempty"`
	VRRPGroup    int          `json:"vrrpGroup,omitempty"`
	VRRPPriority int          `json:"vrrpPriority,omitempty"`

	// Server-only: Address is the bond0 address on the rack VLAN,
	// DefaultGateway is the rack's VRRP virtual gateway and BGPPeers
	// are the physical VlanIPs of the two MLAG leaves.
	Address        netip.Prefix `json:"address,omitempty"`
	DefaultGateway netip.Addr   `json:"defaultGateway,omitempty"`
	BGPPeers       []netip.Addr `json:"bgpPeers,omitempty"`
}

// InterfaceStatus is the observed state of one simulated interface.
type InterfaceStatus struct {
	Name string `json:"name"`
	Up   bool   `json:"up"`
}

// NodeStatus is the observed state of a node, filled by the observer.
// LastObserved is when these values last changed, not the last poll.
// Interface counts and Interfaces cover only the simulated objects:
// topology link endpoints plus modeled logical interfaces (the
// gateway vlanif on leaves, bond0 on servers); container plumbing is
// served separately by the node runtime (management) view.
type NodeStatus struct {
	RuntimeState    RuntimeState      `json:"runtimeState"`
	ContainerID     string            `json:"containerId,omitempty"`
	RouteCount      int               `json:"routeCount"`
	BGPEstablished  int               `json:"bgpEstablished"`
	BGPConfigured   int               `json:"bgpConfigured"`
	InterfacesUp    int               `json:"interfacesUp"`
	InterfacesTotal int               `json:"interfacesTotal"`
	Interfaces      []InterfaceStatus `json:"interfaces,omitempty"`
	VRRPState       string            `json:"vrrpState,omitempty"`
	// AgentState is the node-agent reachability of a running server:
	// AgentUp / AgentDown, empty for non-servers or when unknown.
	AgentState   string    `json:"agentState,omitempty"`
	LastObserved time.Time `json:"lastObserved,omitzero"`
}

// Node-agent reachability as probed by the observer's deep sweep.
const (
	AgentUp   = "Up"
	AgentDown = "Down"
)

// Equal reports whether two observed states match; the Interfaces
// slice makes NodeStatus non-comparable with ==.
func (s NodeStatus) Equal(o NodeStatus) bool {
	return s.RuntimeState == o.RuntimeState &&
		s.ContainerID == o.ContainerID &&
		s.RouteCount == o.RouteCount &&
		s.BGPEstablished == o.BGPEstablished &&
		s.BGPConfigured == o.BGPConfigured &&
		s.InterfacesUp == o.InterfacesUp &&
		s.InterfacesTotal == o.InterfacesTotal &&
		slices.Equal(s.Interfaces, o.Interfaces) &&
		s.VRRPState == o.VRRPState &&
		s.AgentState == o.AgentState &&
		s.LastObserved.Equal(o.LastObserved)
}

// IsRouter reports whether the node runs FRR BGP.
func (s NodeSpec) IsRouter() bool {
	return s.RuntimeType == RuntimeFRR
}
