package service

import (
	"context"
	"errors"
	"log/slog"
	"time"

	kerrors "github.com/go-kratos/kratos/v2/errors"

	"github.com/ifantsai/dcnetlab/internal/biz"
	"github.com/ifantsai/dcnetlab/internal/model"
	"github.com/ifantsai/dcnetlab/internal/topology"
	v1 "github.com/ifantsai/dcnetlab/pb/dcnetlab/v1"
)

// DCNetLabService implements the DCNetLab protobuf service. It talks
// to the biz layer only, dispatching to one usecase per functional
// module; data access always goes through the usecases.
type DCNetLabService struct {
	v1.UnimplementedDCNetLabServer

	labs     *biz.LabUsecase
	topos    *biz.TopologyUsecase
	plans    *biz.PlanUsecase
	ops      *biz.OperationUsecase
	power    *biz.PowerUsecase
	runtime  *biz.RuntimeUsecase
	programs *biz.ProgramUsecase
	packages *biz.PackageUsecase
	log      *slog.Logger
}

// NewDCNetLabService wires the protobuf service.
func NewDCNetLabService(labs *biz.LabUsecase, topos *biz.TopologyUsecase, plans *biz.PlanUsecase, ops *biz.OperationUsecase, power *biz.PowerUsecase, rt *biz.RuntimeUsecase, programs *biz.ProgramUsecase, packages *biz.PackageUsecase, log *slog.Logger) *DCNetLabService {
	return &DCNetLabService{labs: labs, topos: topos, plans: plans, ops: ops, power: power, runtime: rt, programs: programs, packages: packages, log: log}
}

// asAPIError maps biz-layer errors onto Kratos errors so the HTTP
// and gRPC transports return proper status codes.
func asAPIError(err error) error {
	switch {
	case errors.Is(err, biz.ErrNotFound):
		return kerrors.NotFound("NOT_FOUND", err.Error())
	default:
		return kerrors.BadRequest("BAD_REQUEST", err.Error())
	}
}

// --- Labs ---

func (s *DCNetLabService) CreateLab(ctx context.Context, req *v1.CreateLabRequest) (*v1.Lab, error) {
	profile := model.ProfileName(req.Profile)
	if profile == "" {
		profile = model.ProfileMicro
	}

	lab, err := s.labs.CreateLab(req.Name, profile, topologySpecToModel(req.Topology), req.InternetAccess)
	if err != nil {
		return nil, asAPIError(err)
	}

	return labToPB(lab), nil
}

func (s *DCNetLabService) ListLabs(ctx context.Context, _ *v1.ListLabsRequest) (*v1.ListLabsReply, error) {
	labs, err := s.labs.ListLabs()
	if err != nil {
		return nil, asAPIError(err)
	}

	reply := &v1.ListLabsReply{Labs: make([]*v1.Lab, 0, len(labs))}
	for _, l := range labs {
		reply.Labs = append(reply.Labs, labToPB(l))
	}

	return reply, nil
}

func (s *DCNetLabService) GetLab(ctx context.Context, req *v1.GetLabRequest) (*v1.Lab, error) {
	lab, err := s.labs.GetLab(req.Id)
	if err != nil {
		return nil, asAPIError(err)
	}

	return labToPB(lab), nil
}

func (s *DCNetLabService) DeleteLab(ctx context.Context, req *v1.DeleteLabRequest) (*v1.OperationRef, error) {
	op, err := s.labs.DeleteLab(req.Id)
	if err != nil {
		return nil, asAPIError(err)
	}

	return &v1.OperationRef{OperationId: op.ID}, nil
}

func (s *DCNetLabService) StartLab(ctx context.Context, req *v1.StartLabRequest) (*v1.OperationRef, error) {
	op, err := s.power.StartLab(req.Id)
	if err != nil {
		return nil, asAPIError(err)
	}

	return &v1.OperationRef{OperationId: op.ID}, nil
}

func (s *DCNetLabService) StopLab(ctx context.Context, req *v1.StopLabRequest) (*v1.OperationRef, error) {
	op, err := s.power.StopLab(req.Id)
	if err != nil {
		return nil, asAPIError(err)
	}

	return &v1.OperationRef{OperationId: op.ID}, nil
}

func (s *DCNetLabService) StartNode(ctx context.Context, req *v1.StartNodeRequest) (*v1.Node, error) {
	node, err := s.power.StartNode(ctx, req.LabId, req.NodeId)
	if err != nil {
		return nil, asAPIError(err)
	}

	return nodeToPB(node), nil
}

func (s *DCNetLabService) StopNode(ctx context.Context, req *v1.StopNodeRequest) (*v1.Node, error) {
	node, err := s.power.StopNode(ctx, req.LabId, req.NodeId)
	if err != nil {
		return nil, asAPIError(err)
	}

	return nodeToPB(node), nil
}

func (s *DCNetLabService) GetNodeBGP(ctx context.Context, req *v1.GetNodeBGPRequest) (*v1.NodeBGP, error) {
	cfg, err := s.topos.GetNodeBGP(req.LabId, req.NodeId)
	if err != nil {
		return nil, asAPIError(err)
	}

	return nodeBGPToPB(cfg), nil
}

func (s *DCNetLabService) GetNodeRuntime(ctx context.Context, req *v1.GetNodeRuntimeRequest) (*v1.NodeRuntime, error) {
	rt, err := s.runtime.GetNodeRuntime(ctx, req.LabId, req.NodeId)
	if err != nil {
		return nil, asAPIError(err)
	}

	return nodeRuntimeToPB(rt), nil
}

func (s *DCNetLabService) GetNodeRoutes(ctx context.Context, req *v1.GetNodeRoutesRequest) (*v1.NodeRoutes, error) {
	rt, err := s.runtime.GetNodeRoutes(ctx, req.LabId, req.NodeId)
	if err != nil {
		return nil, asAPIError(err)
	}

	return nodeRoutesToPB(rt), nil
}

func (s *DCNetLabService) GetNodeBGPTable(ctx context.Context, req *v1.GetNodeBGPTableRequest) (*v1.NodeBGPTable, error) {
	table, err := s.runtime.GetNodeBGPTable(ctx, req.LabId, req.NodeId)
	if err != nil {
		return nil, asAPIError(err)
	}

	return nodeBGPTableToPB(table), nil
}

func (s *DCNetLabService) GetNodeFIB(ctx context.Context, req *v1.GetNodeFIBRequest) (*v1.NodeFIB, error) {
	rt, err := s.runtime.GetNodeFIB(ctx, req.LabId, req.NodeId)
	if err != nil {
		return nil, asAPIError(err)
	}

	fib := nodeRoutesToPB(rt)

	return &v1.NodeFIB{ContainerState: fib.ContainerState, Routes: fib.Routes}, nil
}

// --- Topology ---

func (s *DCNetLabService) ListNodes(ctx context.Context, req *v1.ListNodesRequest) (*v1.ListNodesReply, error) {
	nodes, err := s.topos.ListNodes(req.LabId)
	if err != nil {
		return nil, asAPIError(err)
	}

	reply := &v1.ListNodesReply{Nodes: make([]*v1.Node, 0, len(nodes))}
	for _, n := range nodes {
		reply.Nodes = append(reply.Nodes, nodeToPB(n))
	}

	return reply, nil
}

func (s *DCNetLabService) ListLinks(ctx context.Context, req *v1.ListLinksRequest) (*v1.ListLinksReply, error) {
	links, err := s.topos.ListLinks(req.LabId)
	if err != nil {
		return nil, asAPIError(err)
	}

	reply := &v1.ListLinksReply{Links: make([]*v1.Link, 0, len(links))}
	for _, l := range links {
		reply.Links = append(reply.Links, linkToPB(l))
	}

	return reply, nil
}

func (s *DCNetLabService) ListAllocations(ctx context.Context, req *v1.ListAllocationsRequest) (*v1.ListAllocationsReply, error) {
	allocs, err := s.topos.ListAllocations(req.LabId)
	if err != nil {
		return nil, asAPIError(err)
	}

	reply := &v1.ListAllocationsReply{Allocations: make([]*v1.Allocation, 0, len(allocs))}
	for _, a := range allocs {
		reply.Allocations = append(reply.Allocations, allocationToPB(a))
	}

	return reply, nil
}

// --- Packages ---

func (s *DCNetLabService) UploadPackage(ctx context.Context, req *v1.UploadPackageRequest) (*v1.Package, error) {
	p, err := s.packages.UploadPackage(req.Payload)
	if err != nil {
		return nil, asAPIError(err)
	}

	return packageToPB(p), nil
}

func (s *DCNetLabService) ListPackages(ctx context.Context, _ *v1.ListPackagesRequest) (*v1.ListPackagesReply, error) {
	packages, err := s.packages.ListPackages()
	if err != nil {
		return nil, asAPIError(err)
	}

	reply := &v1.ListPackagesReply{Packages: make([]*v1.Package, 0, len(packages))}
	for _, p := range packages {
		reply.Packages = append(reply.Packages, packageToPB(p))
	}

	return reply, nil
}

func (s *DCNetLabService) DeletePackage(ctx context.Context, req *v1.DeletePackageRequest) (*v1.DeletePackageReply, error) {
	if err := s.packages.DeletePackage(req.Name, req.Version); err != nil {
		return nil, asAPIError(err)
	}

	return &v1.DeletePackageReply{}, nil
}

func (s *DCNetLabService) InstallPackage(ctx context.Context, req *v1.InstallPackageRequest) (*v1.InstallPackageReply, error) {
	results, err := s.programs.InstallPackageOnServers(ctx, req.LabId, req.Name, req.Version, req.ServerIds)
	if err != nil {
		return nil, asAPIError(err)
	}

	return &v1.InstallPackageReply{Results: serverInstallsToPB(results)}, nil
}

// --- Programs ---

func (s *DCNetLabService) CreateProgram(ctx context.Context, req *v1.CreateProgramRequest) (*v1.CreateProgramReply, error) {
	spec := model.ProgramSpec{
		PackageName:    req.PackageName,
		PackageVersion: req.PackageVersion,
		Args:           req.Args,
		RestartPolicy:  req.RestartPolicy,
		Type:           req.Type,
		AutoStart:      req.AutoStart,
		LivenessCheck:  healthCheckFromPB(req.LivenessCheck),
		ReadinessCheck: healthCheckFromPB(req.ReadinessCheck),
		StartupOrder:   int(req.StartupOrder),
	}

	programs, failures, err := s.programs.CreateProgram(req.LabId, req.Name, spec, req.ServerIds)
	if err != nil {
		return nil, asAPIError(err)
	}

	reply := &v1.CreateProgramReply{}
	for _, p := range programs {
		reply.Programs = append(reply.Programs, programToPB(p))
	}

	reply.Failures = serverInstallsToPB(failures)

	return reply, nil
}

func (s *DCNetLabService) GetNodeInventory(ctx context.Context, req *v1.GetNodeInventoryRequest) (*v1.NodeInventory, error) {
	inv, err := s.programs.NodeInventory(ctx, req.LabId, req.NodeId)
	if err != nil {
		return nil, asAPIError(err)
	}

	return inventoryToPB(inv), nil
}

func (s *DCNetLabService) GetNodeMetrics(ctx context.Context, req *v1.GetNodeMetricsRequest) (*v1.NodeMetrics, error) {
	metrics, err := s.programs.NodeMetrics(ctx, req.LabId, req.NodeId)
	if err != nil {
		return nil, asAPIError(err)
	}

	return nodeMetricsToPB(metrics), nil
}

func (s *DCNetLabService) GetNodeMetricsHistory(ctx context.Context, req *v1.GetNodeMetricsHistoryRequest) (*v1.NodeMetricsHistory, error) {
	var start, end time.Time
	if req.Start > 0 {
		start = time.Unix(req.Start, 0).UTC()
	}

	if req.End > 0 {
		end = time.Unix(req.End, 0).UTC()
	}

	points, err := s.programs.NodeMetricsHistory(req.LabId, req.NodeId, start, end)
	if err != nil {
		return nil, asAPIError(err)
	}

	reply := &v1.NodeMetricsHistory{Points: make([]*v1.MetricsPoint, 0, len(points))}
	for _, p := range points {
		reply.Points = append(reply.Points, metricsPointToPB(p))
	}

	return reply, nil
}

func (s *DCNetLabService) ListPrograms(ctx context.Context, req *v1.ListProgramsRequest) (*v1.ListProgramsReply, error) {
	programs, err := s.programs.ListPrograms(ctx, req.LabId)
	if err != nil {
		return nil, asAPIError(err)
	}

	reply := &v1.ListProgramsReply{Programs: make([]*v1.Program, 0, len(programs))}
	for _, p := range programs {
		reply.Programs = append(reply.Programs, programToPB(p))
	}

	return reply, nil
}

func (s *DCNetLabService) StartProgram(ctx context.Context, req *v1.ProgramOpRequest) (*v1.Program, error) {
	p, err := s.programs.StartProgram(ctx, req.LabId, req.Id)
	if err != nil {
		return nil, asAPIError(err)
	}

	return programToPB(p), nil
}

func (s *DCNetLabService) StopProgram(ctx context.Context, req *v1.ProgramOpRequest) (*v1.Program, error) {
	p, err := s.programs.StopProgram(ctx, req.LabId, req.Id)
	if err != nil {
		return nil, asAPIError(err)
	}

	return programToPB(p), nil
}

func (s *DCNetLabService) UpgradeProgram(ctx context.Context, req *v1.UpgradeProgramRequest) (*v1.Program, error) {
	p, err := s.programs.UpgradeProgram(ctx, req.LabId, req.Id, req.Version)
	if err != nil {
		return nil, asAPIError(err)
	}

	return programToPB(p), nil
}

func (s *DCNetLabService) DeleteProgram(ctx context.Context, req *v1.ProgramOpRequest) (*v1.DeleteProgramReply, error) {
	if err := s.programs.DeleteProgram(ctx, req.LabId, req.Id); err != nil {
		return nil, asAPIError(err)
	}

	return &v1.DeleteProgramReply{}, nil
}

func (s *DCNetLabService) GetProgramLogs(ctx context.Context, req *v1.GetProgramLogsRequest) (*v1.ProgramLogs, error) {
	content, err := s.programs.GetProgramLogs(ctx, req.LabId, req.Id, int(req.Tail))
	if err != nil {
		return nil, asAPIError(err)
	}

	return &v1.ProgramLogs{Content: content}, nil
}

// --- Plans ---

func (s *DCNetLabService) CreatePlan(ctx context.Context, req *v1.CreatePlanRequest) (*v1.Plan, error) {
	plan, err := s.plans.CreatePlan(req.LabId)
	if err != nil {
		return nil, asAPIError(err)
	}

	return planToPB(plan), nil
}

func (s *DCNetLabService) GetPlan(ctx context.Context, req *v1.GetPlanRequest) (*v1.Plan, error) {
	plan, err := s.plans.GetPlan(req.Id)
	if err != nil {
		return nil, asAPIError(err)
	}

	return planToPB(plan), nil
}

func (s *DCNetLabService) ApplyPlan(ctx context.Context, req *v1.ApplyPlanRequest) (*v1.OperationRef, error) {
	op, err := s.plans.ApplyPlan(req.Id)
	if err != nil {
		return nil, asAPIError(err)
	}

	return &v1.OperationRef{OperationId: op.ID}, nil
}

// --- Operations & Generations ---

func (s *DCNetLabService) GetOperation(ctx context.Context, req *v1.GetOperationRequest) (*v1.Operation, error) {
	op, err := s.ops.GetOperation(req.Id)
	if err != nil {
		return nil, asAPIError(err)
	}

	return operationToPB(op), nil
}

func (s *DCNetLabService) ListOperations(ctx context.Context, req *v1.ListOperationsRequest) (*v1.ListOperationsReply, error) {
	ops, err := s.ops.ListOperations(req.LabId)
	if err != nil {
		return nil, asAPIError(err)
	}

	reply := &v1.ListOperationsReply{Operations: make([]*v1.Operation, 0, len(ops))}
	for _, op := range ops {
		reply.Operations = append(reply.Operations, operationToPB(op))
	}

	return reply, nil
}

func (s *DCNetLabService) ListGenerations(ctx context.Context, req *v1.ListGenerationsRequest) (*v1.ListGenerationsReply, error) {
	gens, err := s.plans.ListGenerations(req.LabId)
	if err != nil {
		return nil, asAPIError(err)
	}

	if gens == nil {
		gens = []int64{}
	}

	return &v1.ListGenerationsReply{Generations: gens}, nil
}

// --- Profiles & health ---

func (s *DCNetLabService) ListProfiles(ctx context.Context, _ *v1.ListProfilesRequest) (*v1.ListProfilesReply, error) {
	all := topology.Profiles()
	reply := &v1.ListProfilesReply{}
	for _, name := range []model.ProfileName{model.ProfileMicro, model.ProfileStandard} {
		topo := all[name]
		reply.Profiles = append(reply.Profiles, &v1.ProfileInfo{
			Name:     string(name),
			Topology: topologySpecToPB(topo),
		})
	}

	return reply, nil
}

func (s *DCNetLabService) Healthz(ctx context.Context, _ *v1.HealthzRequest) (*v1.HealthzReply, error) {
	return &v1.HealthzReply{Status: "ok"}, nil
}
