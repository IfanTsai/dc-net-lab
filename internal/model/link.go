package model

import "net/netip"

// LinkEndpoint is one side of a point-to-point link.
type LinkEndpoint struct {
	NodeID    string       `json:"nodeId"`
	NodeName  string       `json:"nodeName"`
	Interface string       `json:"interface"`
	Address   netip.Prefix `json:"address,omitempty"`
}

// Link is a point-to-point link between two nodes.
type Link struct {
	Meta   ResourceMeta `json:"meta"`
	Spec   LinkSpec     `json:"spec"`
	Status LinkStatus   `json:"status"`
}

// LinkKind classifies how a link is used in the fabric.
type LinkKind string

const (
	// LinkFabric is a routed /31 point-to-point link between routers.
	LinkFabric LinkKind = "fabric"
	// LinkServerAccess is an L2 access port (untagged, PVID = VlanID)
	// from a leaf to one bond member of a server.
	LinkServerAccess LinkKind = "server-access"
	// LinkMLAGPeer is the L2 peer link between the two MLAG leaves of
	// a rack, trunking the server VLAN.
	LinkMLAGPeer LinkKind = "mlag-peer"
)

// LinkSpec is the desired state of a link. L2 links (server-access,
// mlag-peer) carry no endpoint addresses; addressing lives on the
// VLAN interface (leaf) and bond interface (server).
type LinkSpec struct {
	LabID     string       `json:"labId"`
	Kind      LinkKind     `json:"kind,omitempty"`
	VlanID    int          `json:"vlanId,omitempty"`
	EndpointA LinkEndpoint `json:"endpointA"`
	EndpointB LinkEndpoint `json:"endpointB"`
	MTU       int          `json:"mtu"`
}

// LinkStatus is the observed state of a link.
type LinkStatus struct {
	AdminUp bool `json:"adminUp"`
	OperUp  bool `json:"operUp"`
}
