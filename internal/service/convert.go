package service

import (
	"net/netip"
	"time"

	"google.golang.org/protobuf/types/known/timestamppb"

	"github.com/ifantsai/dcnetlab/internal/biz"
	"github.com/ifantsai/dcnetlab/internal/compiler/frr"
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

	status := &v1.NodeStatus{
		RuntimeState:    string(n.Status.RuntimeState),
		ContainerId:     n.Status.ContainerID,
		RouteCount:      int32(n.Status.RouteCount),
		BgpEstablished:  int32(n.Status.BGPEstablished),
		BgpConfigured:   int32(n.Status.BGPConfigured),
		InterfacesUp:    int32(n.Status.InterfacesUp),
		InterfacesTotal: int32(n.Status.InterfacesTotal),
		VrrpState:       n.Status.VRRPState,
		LastObserved:    timePB(n.Status.LastObserved),
	}

	for _, it := range n.Status.Interfaces {
		status.Interfaces = append(status.Interfaces, &v1.InterfaceStatus{Name: it.Name, Up: it.Up})
	}

	return &v1.Node{
		Meta:   metaToPB(n.Meta),
		Spec:   spec,
		Status: status,
	}
}

func nodeBGPToPB(cfg *frr.RouterConfig) *v1.NodeBGP {
	pb := &v1.NodeBGP{
		Asn:      cfg.ASN,
		RouterId: addrString(cfg.RouterID),
	}

	for _, nb := range cfg.Neighbors {
		pb.Neighbors = append(pb.Neighbors, &v1.BGPNeighbor{
			Address:     addrString(nb.Address),
			RemoteAs:    nb.RemoteAS,
			Description: nb.Name,
		})
	}

	if cfg.ServerGroup != nil {
		pb.ServerGroup = &v1.BGPServerGroup{
			RemoteAs:    cfg.ServerGroup.ASN,
			ListenRange: prefixString(cfg.ServerGroup.ListenRange),
		}
	}

	return pb
}

func nodeRoutesToPB(rt *biz.NodeRoutes) *v1.NodeRoutes {
	pb := &v1.NodeRoutes{ContainerState: rt.ContainerState}
	for _, r := range rt.Routes {
		route := &v1.Route{
			Prefix:   r.Prefix,
			Protocol: r.Protocol,
			Kind:     r.Kind,
			Selected: r.Selected,
			Distance: int32(r.Distance),
			Metric:   int32(r.Metric),
		}

		for _, nh := range r.Nexthops {
			route.Nexthops = append(route.Nexthops, &v1.RouteNexthop{
				Via:       nh.Via,
				Interface: nh.Interface,
				Active:    nh.Active,
			})
		}

		pb.Routes = append(pb.Routes, route)
	}

	return pb
}

func nodeBGPTableToPB(table *biz.NodeBGPTable) *v1.NodeBGPTable {
	pb := &v1.NodeBGPTable{
		ContainerState: table.ContainerState,
		RouterId:       table.RouterID,
		LocalAs:        table.LocalAS,
	}

	for _, p := range table.Paths {
		pb.Paths = append(pb.Paths, &v1.BGPPath{
			Prefix:      p.Prefix,
			Best:        p.Best,
			Multipath:   p.Multipath,
			Valid:       p.Valid,
			Internal:    p.Internal,
			AsPath:      p.ASPath,
			Origin:      p.Origin,
			LocalPref:   p.LocalPref,
			Peer:        p.Peer,
			Nexthop:     p.Nexthop,
			NexthopName: p.NexthopName,
		})
	}

	return pb
}

func nodeRuntimeToPB(rt *biz.NodeRuntime) *v1.NodeRuntime {
	pb := &v1.NodeRuntime{ContainerState: rt.ContainerState}
	for _, it := range rt.Interfaces {
		pb.Interfaces = append(pb.Interfaces, &v1.RuntimeInterface{
			Name:      it.Name,
			State:     it.State,
			Mac:       it.MAC,
			Addresses: it.Addresses,
		})
	}

	return pb
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

// --- Package ---

func packageToPB(p *model.Package) *v1.Package {
	return &v1.Package{
		Meta:        metaToPB(p.Meta),
		Version:     p.Spec.Version,
		Format:      p.Spec.Format,
		Entrypoint:  p.Spec.Entrypoint,
		Description: p.Spec.Description,
		Sha256:      p.Status.SHA256,
		SizeBytes:   p.Status.SizeBytes,
		Builtin:     p.Status.Builtin,
	}
}

// --- Program ---

func programToPB(p *model.Program) *v1.Program {
	status := &v1.ProgramStatus{
		State:        p.Status.State,
		Pid:          int32(p.Status.PID),
		Restarts:     int32(p.Status.Restarts),
		LastError:    p.Status.LastError,
		StartedAt:    timePB(p.Status.StartedAt),
		LastObserved: timePB(p.Status.LastObserved),
	}

	return &v1.Program{
		Meta: metaToPB(p.Meta),
		Spec: &v1.ProgramSpec{
			LabId:          p.Spec.LabID,
			ServerId:       p.Spec.ServerID,
			ServerName:     p.Spec.ServerName,
			PackageName:    p.Spec.PackageName,
			PackageVersion: p.Spec.PackageVersion,
			Entrypoint:     p.Spec.Entrypoint,
			Args:           p.Spec.Args,
			RestartPolicy:  p.Spec.RestartPolicy,
			Type:           p.Spec.Type,
			AutoStart:      p.Spec.AutoStart,
			DesiredState:   p.Spec.DesiredState,
		},
		Status: status,
	}
}

func serverInstallsToPB(results []biz.ServerInstall) []*v1.ServerInstallResult {
	pbs := make([]*v1.ServerInstallResult, 0, len(results))
	for _, r := range results {
		pb := &v1.ServerInstallResult{ServerId: r.ServerID, ServerName: r.ServerName}
		if r.Err != nil {
			pb.Error = r.Err.Error()
		}

		pbs = append(pbs, pb)
	}

	return pbs
}

func inventoryToPB(inv *model.NodeInventory) *v1.NodeInventory {
	pb := &v1.NodeInventory{}
	for _, p := range inv.Packages {
		pb.Packages = append(pb.Packages, &v1.InstalledPackage{
			Name: p.Name, Version: p.Version, Sha256: p.SHA256,
		})
	}

	for _, p := range inv.Programs {
		pb.Programs = append(pb.Programs, &v1.NodeProgram{
			Name:           p.Name,
			PackageName:    p.PackageName,
			PackageVersion: p.PackageVersion,
			Entrypoint:     p.Entrypoint,
			Args:           p.Args,
			RestartPolicy:  p.RestartPolicy,
			Type:           p.Type,
			AutoStart:      p.AutoStart,
			State:          p.State,
			Pid:            int32(p.PID),
			Restarts:       int32(p.Restarts),
			LastError:      p.LastError,
			Managed:        p.Managed,
		})
	}

	return pb
}

func nodeMetricsToPB(m *model.NodeMetrics) *v1.NodeMetrics {
	return &v1.NodeMetrics{
		SampledAt:     m.SampledAt.Unix(),
		UptimeSeconds: int64(m.Uptime.Seconds()),
		Procs:         int64(m.Procs),
		Cpu:           metricsCPUToPB(m.CPU),
		Memory:        metricsMemoryToPB(m.Memory),
		Load:          metricsLoadToPB(m.Load),
		Filesystem:    metricsFilesystemToPB(m.Filesystem),
		Disk:          metricsDiskToPB(m.Disk),
		Interfaces:    metricsInterfacesToPB(m.Interfaces),
	}
}

func metricsPointToPB(p model.MetricsPoint) *v1.MetricsPoint {
	return &v1.MetricsPoint{
		Ts:         p.Ts.Unix(),
		Procs:      int64(p.Procs),
		Cpu:        metricsCPUToPB(p.CPU),
		Memory:     metricsMemoryToPB(p.Memory),
		Load:       metricsLoadToPB(p.Load),
		Filesystem: metricsFilesystemToPB(p.Filesystem),
		Disk:       metricsDiskToPB(p.Disk),
		Interfaces: metricsInterfacesToPB(p.Interfaces),
	}
}

func metricsCPUToPB(c model.MetricsCPU) *v1.MetricsCPU {
	return &v1.MetricsCPU{
		UsagePercent:       c.UsagePercent,
		UserPercent:        c.UserPercent,
		SystemPercent:      c.SystemPercent,
		LimitCores:         c.LimitCores,
		UsageSecondsTotal:  c.UsageSecondsTotal,
		UserSecondsTotal:   c.UserSecondsTotal,
		SystemSecondsTotal: c.SystemSecondsTotal,
	}
}

func metricsMemoryToPB(m model.MetricsMemory) *v1.MetricsMemory {
	return &v1.MetricsMemory{
		UsedBytes:     m.UsedBytes,
		CacheBytes:    m.CacheBytes,
		LimitBytes:    m.LimitBytes,
		SwapUsedBytes: m.SwapUsedBytes,
	}
}

func metricsLoadToPB(l model.MetricsLoad) *v1.MetricsLoad {
	return &v1.MetricsLoad{Load1: l.Load1, Load5: l.Load5, Load15: l.Load15}
}

func metricsFilesystemToPB(fs model.MetricsFilesystem) *v1.MetricsFilesystem {
	return &v1.MetricsFilesystem{
		Mount:      fs.Mount,
		SizeBytes:  fs.SizeBytes,
		UsedBytes:  fs.UsedBytes,
		AvailBytes: fs.AvailBytes,
	}
}

func metricsDiskToPB(d model.MetricsDisk) *v1.MetricsDisk {
	return &v1.MetricsDisk{
		ReadBytesPerSec:  d.ReadBytesPerSec,
		WriteBytesPerSec: d.WriteBytesPerSec,
		ReadOpsPerSec:    d.ReadOpsPerSec,
		WriteOpsPerSec:   d.WriteOpsPerSec,
		ReadBytesTotal:   d.ReadBytesTotal,
		WriteBytesTotal:  d.WriteBytesTotal,
		ReadOpsTotal:     d.ReadOpsTotal,
		WriteOpsTotal:    d.WriteOpsTotal,
	}
}

func metricsInterfacesToPB(ifaces []model.MetricsInterface) []*v1.MetricsInterface {
	pbs := make([]*v1.MetricsInterface, 0, len(ifaces))
	for _, iface := range ifaces {
		pbs = append(pbs, &v1.MetricsInterface{
			Name:            iface.Name,
			RxBytesPerSec:   iface.RxBytesPerSec,
			TxBytesPerSec:   iface.TxBytesPerSec,
			RxPacketsPerSec: iface.RxPacketsPerSec,
			TxPacketsPerSec: iface.TxPacketsPerSec,
			RxBytesTotal:    iface.RxBytesTotal,
			TxBytesTotal:    iface.TxBytesTotal,
			RxPacketsTotal:  iface.RxPacketsTotal,
			TxPacketsTotal:  iface.TxPacketsTotal,
			RxErrors:        iface.RxErrors,
			TxErrors:        iface.TxErrors,
			RxDropped:       iface.RxDropped,
			TxDropped:       iface.TxDropped,
		})
	}

	return pbs
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
