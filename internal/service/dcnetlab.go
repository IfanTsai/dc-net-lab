package service

import (
	"context"
	"errors"
	"log/slog"

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

	labs  *biz.LabUsecase
	topos *biz.TopologyUsecase
	plans *biz.PlanUsecase
	ops   *biz.OperationUsecase
	power *biz.PowerUsecase
	log   *slog.Logger
}

// NewDCNetLabService wires the protobuf service.
func NewDCNetLabService(labs *biz.LabUsecase, topos *biz.TopologyUsecase, plans *biz.PlanUsecase, ops *biz.OperationUsecase, power *biz.PowerUsecase, log *slog.Logger) *DCNetLabService {
	return &DCNetLabService{labs: labs, topos: topos, plans: plans, ops: ops, power: power, log: log}
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

	lab, err := s.labs.CreateLab(req.Name, profile, topologySpecToModel(req.Topology))
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
