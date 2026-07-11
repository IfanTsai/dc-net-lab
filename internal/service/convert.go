package service

import (
	"net/netip"
	"time"

	"google.golang.org/protobuf/types/known/timestamppb"

	"github.com/ifantsai/dcnetlab/internal/model"
	v1 "github.com/ifantsai/dcnetlab/pb/dcnetlab/v1"
)

// Converters between the internal resource model and the protobuf
// API. Addresses are rendered as strings ("" = unset); the model side
// stays on netip so the compiler and allocators keep typed addresses.

func prefixString(p netip.Prefix) string {
	if !p.IsValid() {
		return ""
	}

	return p.String()
}

func addrString(a netip.Addr) string {
	if !a.IsValid() {
		return ""
	}

	return a.String()
}

func timePB(t time.Time) *timestamppb.Timestamp {
	if t.IsZero() {
		return nil
	}

	return timestamppb.New(t)
}

func timePtrPB(t *time.Time) *timestamppb.Timestamp {
	if t == nil {
		return nil
	}

	return timePB(*t)
}

func metaToPB(m model.ResourceMeta) *v1.ResourceMeta {
	pb := &v1.ResourceMeta{
		Id:                 m.ID,
		Name:               m.Name,
		Generation:         m.Generation,
		ObservedGeneration: m.ObservedGeneration,
		Phase:              string(m.Phase),
		CreatedAt:          timePB(m.CreatedAt),
		UpdatedAt:          timePB(m.UpdatedAt),
	}

	if m.LastError != nil {
		pb.LastError = &v1.ResourceError{
			Code:    m.LastError.Code,
			Message: m.LastError.Message,
			Time:    timePB(m.LastError.Time),
		}
	}

	return pb
}

// --- Lab ---

func topologySpecToPB(t model.TopologySpec) *v1.TopologySpec {
	pb := &v1.TopologySpec{
		ExternalRouters: int32(t.ExternalRouters),
		DcEdges:         int32(t.DCEdges),
		SuperSpines:     int32(t.SuperSpines),
	}

	for _, p := range t.Pods {
		pb.Pods = append(pb.Pods, &v1.PodSpec{
			Name:           p.Name,
			Spines:         int32(p.Spines),
			Racks:          int32(p.Racks),
			ServersPerRack: int32(p.ServersPerRack),
		})
	}

	return pb
}

func topologySpecToModel(pb *v1.TopologySpec) *model.TopologySpec {
	if pb == nil {
		return nil
	}

	t := &model.TopologySpec{
		ExternalRouters: int(pb.ExternalRouters),
		DCEdges:         int(pb.DcEdges),
		SuperSpines:     int(pb.SuperSpines),
	}

	for _, p := range pb.Pods {
		t.Pods = append(t.Pods, model.PodSpec{
			Name:           p.Name,
			Spines:         int(p.Spines),
			Racks:          int(p.Racks),
			ServersPerRack: int(p.ServersPerRack),
		})
	}

	return t
}

func labToPB(l *model.Lab) *v1.Lab {
	spec := &v1.LabSpec{
		Profile:  string(l.Spec.Profile),
		Topology: topologySpecToPB(l.Spec.Topology),
	}

	for _, p := range l.Spec.Pools {
		spec.Pools = append(spec.Pools, &v1.AddressPool{
			Name:             string(p.Name),
			Cidr:             p.CIDR,
			AllocationPrefix: int32(p.AllocationPrefix),
		})
	}

	for _, r := range l.Spec.ASNs {
		spec.Asns = append(spec.Asns, &v1.ASNRange{
			Role:  string(r.Role),
			Start: r.Start,
			End:   r.End,
		})
	}

	return &v1.Lab{
		Meta: metaToPB(l.Meta),
		Spec: spec,
		Status: &v1.LabStatus{
			NodeCount: int32(l.Status.NodeCount),
			LinkCount: int32(l.Status.LinkCount),
		},
	}
}

// --- Node ---

func nodeToPB(n *model.Node) *v1.Node {
	spec := &v1.NodeSpec{
		LabId:          n.Spec.LabID,
		Role:           string(n.Spec.Role),
		PodId:          n.Spec.PodID,
		RackId:         n.Spec.RackID,
		Asn:            n.Spec.ASN,
		Loopback:       prefixString(n.Spec.Loopback),
		MgmtIp:         addrString(n.Spec.MgmtIP),
		RuntimeType:    string(n.Spec.RuntimeType),
		MlagPeer:       n.Spec.MLAGPeer,
		VlanId:         int32(n.Spec.VlanID),
		VlanIp:         prefixString(n.Spec.VlanIP),
		GatewayIp:      addrString(n.Spec.GatewayIP),
		GatewayMac:     n.Spec.GatewayMAC,
		VrrpGroup:      int32(n.Spec.VRRPGroup),
		VrrpPriority:   int32(n.Spec.VRRPPriority),
		Address:        prefixString(n.Spec.Address),
		DefaultGateway: addrString(n.Spec.DefaultGateway),
	}

	for _, p := range n.Spec.BGPPeers {
		spec.BgpPeers = append(spec.BgpPeers, addrString(p))
	}

	return &v1.Node{
		Meta: metaToPB(n.Meta),
		Spec: spec,
		Status: &v1.NodeStatus{
			RuntimeState:    string(n.Status.RuntimeState),
			ContainerId:     n.Status.ContainerID,
			RouteCount:      int32(n.Status.RouteCount),
			BgpEstablished:  int32(n.Status.BGPEstablished),
			BgpConfigured:   int32(n.Status.BGPConfigured),
			InterfacesUp:    int32(n.Status.InterfacesUp),
			InterfacesTotal: int32(n.Status.InterfacesTotal),
			LastObserved:    timePB(n.Status.LastObserved),
		},
	}
}

// --- Link ---

func endpointToPB(e model.LinkEndpoint) *v1.LinkEndpoint {
	return &v1.LinkEndpoint{
		NodeId:    e.NodeID,
		NodeName:  e.NodeName,
		Interface: e.Interface,
		Address:   prefixString(e.Address),
	}
}

func linkToPB(l *model.Link) *v1.Link {
	return &v1.Link{
		Meta: metaToPB(l.Meta),
		Spec: &v1.LinkSpec{
			LabId:     l.Spec.LabID,
			Kind:      string(l.Spec.Kind),
			VlanId:    int32(l.Spec.VlanID),
			EndpointA: endpointToPB(l.Spec.EndpointA),
			EndpointB: endpointToPB(l.Spec.EndpointB),
			Mtu:       int32(l.Spec.MTU),
		},
		Status: &v1.LinkStatus{
			AdminUp: l.Status.AdminUp,
			OperUp:  l.Status.OperUp,
		},
	}
}

// --- Plan ---

func allocationToPB(a model.Allocation) *v1.Allocation {
	return &v1.Allocation{Pool: a.Pool, Value: a.Value, Owner: a.Owner}
}

func planToPB(p *model.Plan) *v1.Plan {
	pb := &v1.Plan{
		Id:             p.ID,
		LabId:          p.LabID,
		BaseGeneration: p.BaseGeneration,
		NewGeneration:  p.NewGeneration,
		State:          string(p.State),
		CreatedAt:      timePB(p.CreatedAt),
	}

	for _, op := range p.Operations {
		pb.Operations = append(pb.Operations, &v1.PlanOperation{
			Type:    string(op.Type),
			Target:  op.Target,
			Summary: op.Summary,
		})
	}

	for _, a := range p.Allocations {
		pb.Allocations = append(pb.Allocations, allocationToPB(a))
	}

	for _, w := range p.Warnings {
		pb.Warnings = append(pb.Warnings, &v1.PlanWarning{Code: w.Code, Message: w.Message})
	}

	return pb
}

// --- Operation ---

func operationToPB(op *model.Operation) *v1.Operation {
	pb := &v1.Operation{
		Id:    op.ID,
		LabId: op.LabID,
		Type:  string(op.Type),
		Resource: &v1.ResourceRef{
			Type: op.Resource.Type,
			Id:   op.Resource.ID,
		},
		State:     string(op.State),
		Progress:  int32(op.Progress),
		CreatedAt: timePB(op.CreatedAt),
		UpdatedAt: timePB(op.UpdatedAt),
	}

	for _, st := range op.Steps {
		pb.Steps = append(pb.Steps, &v1.OperationStep{
			Name:       st.Name,
			State:      string(st.State),
			Message:    st.Message,
			StartedAt:  timePtrPB(st.StartedAt),
			FinishedAt: timePtrPB(st.FinishedAt),
		})
	}

	if op.Error != nil {
		pb.Error = &v1.OperationError{Code: op.Error.Code, Message: op.Error.Message}
	}

	return pb
}
