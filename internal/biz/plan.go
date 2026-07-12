package biz

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"time"

	"github.com/ifantsai/dcnetlab/internal/compiler"
	"github.com/ifantsai/dcnetlab/internal/compiler/containerlab"
	"github.com/ifantsai/dcnetlab/internal/conf"
	"github.com/ifantsai/dcnetlab/internal/model"
	"github.com/ifantsai/dcnetlab/internal/operation"
	"github.com/ifantsai/dcnetlab/internal/runtime"
	"github.com/ifantsai/dcnetlab/internal/topology"
)

// DesiredStateSnapshot is the persisted content of one generation:
// the lab and its full desired topology at apply time.
type DesiredStateSnapshot struct {
	Lab   *model.Lab    `json:"lab"`
	Nodes []*model.Node `json:"nodes"`
	Links []*model.Link `json:"links"`
}

// PlanRepo abstracts the persistence the plan usecase needs: plans
// and generations plus the lab and topology state a plan/apply
// transaction spans.
type PlanRepo interface {
	GetLab(id string) (*model.Lab, error)
	UpdateLab(lab *model.Lab) error
	ReplaceTopology(labID string, nodes []*model.Node, links []*model.Link, allocs []model.Allocation) error
	ListNodes(labID string) ([]*model.Node, error)
	ListLinks(labID string) ([]*model.Link, error)
	UpdateNode(n *model.Node) error
	CreatePlan(p *model.Plan) error
	UpdatePlan(p *model.Plan) error
	GetPlan(id string) (*model.Plan, error)
	SaveGeneration(labID string, generation int64, snap *DesiredStateSnapshot) error
	ListGenerations(labID string) ([]int64, error)
}

// PlanUsecase owns the declarative change flow: compute a previewable
// plan, then apply it (compile artifacts, deploy, validate).
type PlanUsecase struct {
	repo     PlanRepo
	ops      *operation.Manager
	driver   runtime.Driver
	programs *ProgramUsecase
	dataDir  string
	binDir   string
	log      *slog.Logger
}

// NewPlanUsecase wires the plan usecase.
func NewPlanUsecase(repo PlanRepo, ops *operation.Manager, driver runtime.Driver, programs *ProgramUsecase, c *conf.Data, log *slog.Logger) *PlanUsecase {
	return &PlanUsecase{repo: repo, ops: ops, driver: driver, programs: programs, dataDir: c.Dir, binDir: c.BinDir, log: log}
}

// CreatePlan builds the desired topology for the lab, persists it as
// desired state and returns a previewable plan.
func (uc *PlanUsecase) CreatePlan(labID string) (*model.Plan, error) {
	lab, err := uc.repo.GetLab(labID)
	if err != nil {
		return nil, fmt.Errorf("get lab: %w", err)
	}

	builder, err := topology.NewBuilder(lab.Meta.ID, lab.Spec)
	if err != nil {
		return nil, fmt.Errorf("create topology builder: %w", err)
	}

	res, err := builder.Build(lab.Spec.Topology)
	if err != nil {
		return nil, fmt.Errorf("build topology: %w", err)
	}

	plan := &model.Plan{
		ID:             model.NewID("plan"),
		LabID:          lab.Meta.ID,
		BaseGeneration: lab.Meta.Generation,
		NewGeneration:  lab.Meta.Generation + 1,
		State:          model.PlanPending,
		Allocations:    res.Allocations,
		CreatedAt:      time.Now().UTC(),
	}

	for _, n := range res.Nodes {
		summary := fmt.Sprintf("%s (%s)", n.Meta.Name, n.Spec.Role)
		if n.Spec.IsRouter() {
			summary = fmt.Sprintf("%s (%s, AS%d, lo %s)", n.Meta.Name, n.Spec.Role, n.Spec.ASN, n.Spec.Loopback)
		}

		plan.Operations = append(plan.Operations, model.PlanOperation{
			Type: model.PlanCreateNode, Target: n.Meta.Name, Summary: summary,
		})
	}

	for _, l := range res.Links {
		plan.Operations = append(plan.Operations, model.PlanOperation{
			Type:   model.PlanCreateLink,
			Target: l.Meta.Name,
			Summary: fmt.Sprintf("%s:%s (%s) <-> %s:%s (%s)",
				l.Spec.EndpointA.NodeName, l.Spec.EndpointA.Interface, l.Spec.EndpointA.Address,
				l.Spec.EndpointB.NodeName, l.Spec.EndpointB.Interface, l.Spec.EndpointB.Address),
		})
	}

	plan.Operations = append(plan.Operations,
		model.PlanOperation{Type: model.PlanRenderConfig, Target: lab.Meta.Name, Summary: "render FRR and Containerlab artifacts"},
		model.PlanOperation{Type: model.PlanDeployTopology, Target: lab.Meta.Name, Summary: "deploy topology via " + uc.driver.Name()},
	)

	// The first release replaces the whole desired topology per plan;
	// desired state is persisted at plan time so the plan is exactly
	// what apply will deploy.
	if err := uc.repo.ReplaceTopology(lab.Meta.ID, res.Nodes, res.Links, res.Allocations); err != nil {
		return nil, fmt.Errorf("persist desired topology: %w", err)
	}

	if err := uc.repo.CreatePlan(plan); err != nil {
		return nil, fmt.Errorf("persist plan: %w", err)
	}

	lab.Meta.Phase = model.PhasePlanning
	if err := uc.repo.UpdateLab(lab); err != nil {
		return nil, fmt.Errorf("update lab phase: %w", err)
	}

	return plan, nil
}

// GetPlan returns one plan by ID.
func (uc *PlanUsecase) GetPlan(id string) (*model.Plan, error) { return uc.repo.GetPlan(id) }

// ListGenerations returns the stored generation numbers of a lab.
func (uc *PlanUsecase) ListGenerations(labID string) ([]int64, error) {
	return uc.repo.ListGenerations(labID)
}

// ApplyPlan compiles artifacts, saves the generation snapshot and
// deploys the topology. It returns immediately with an operation.
func (uc *PlanUsecase) ApplyPlan(planID string) (*model.Operation, error) {
	plan, err := uc.repo.GetPlan(planID)
	if err != nil {
		return nil, fmt.Errorf("get plan: %w", err)
	}

	if plan.State != model.PlanPending {
		return nil, fmt.Errorf("plan %s is %s, only pending plans can be applied", planID, plan.State)
	}

	lab, err := uc.repo.GetLab(plan.LabID)
	if err != nil {
		return nil, fmt.Errorf("get lab: %w", err)
	}

	if lab.Meta.Generation != plan.BaseGeneration {
		return nil, fmt.Errorf("plan %s is based on generation %d but lab is at %d",
			planID, plan.BaseGeneration, lab.Meta.Generation)
	}

	nodes, err := uc.repo.ListNodes(lab.Meta.ID)
	if err != nil {
		return nil, fmt.Errorf("list nodes: %w", err)
	}

	links, err := uc.repo.ListLinks(lab.Meta.ID)
	if err != nil {
		return nil, fmt.Errorf("list links: %w", err)
	}

	op, err := uc.ops.Create(lab.Meta.ID, model.OperationApplyPlan, model.ResourceRef{Type: "plan", ID: planID})
	if err != nil {
		return nil, fmt.Errorf("create apply operation: %w", err)
	}

	lab.Meta.Phase = model.PhaseApplying
	if err := uc.repo.UpdateLab(lab); err != nil {
		return nil, fmt.Errorf("update lab phase: %w", err)
	}

	genDir := generationDir(uc.dataDir, lab.Meta.ID, plan.NewGeneration)
	opts := containerlab.DefaultOptions()
	opts.HostBinDir = uc.binDir
	var artifact *compiler.Artifact

	var steps []operation.Step
	if lab.Spec.Topology.InternetAccess {
		steps = append(steps, operation.Step{Name: "EnsureEdgeImage", Fn: func(ctx context.Context) error {
			if err := uc.driver.EnsureImage(ctx, opts.EdgeImage); err != nil {
				return fmt.Errorf("internet access needs the edge image, build it with `make edge-image`: %w", err)
			}

			return nil
		}})
	}

	steps = append(steps, []operation.Step{
		{Name: "CompileArtifacts", Fn: func(ctx context.Context) error {
			a, err := compiler.Compile(lab, nodes, links, opts)
			artifact = a

			return err
		}},
		{Name: "WriteArtifacts", Fn: func(ctx context.Context) error {
			return runtime.WriteArtifact(artifact, genDir)
		}},
		{Name: "SaveGeneration", Fn: func(ctx context.Context) error {
			return uc.repo.SaveGeneration(lab.Meta.ID, plan.NewGeneration,
				&DesiredStateSnapshot{Lab: lab, Nodes: nodes, Links: links})
		}},
		{Name: "DeployTopology", Fn: func(ctx context.Context) error {
			return uc.driver.Deploy(ctx, genDir)
		}},
		{Name: "ConnectInternet", Fn: func(ctx context.Context) error {
			return uc.connectInternet(ctx, lab, nodes)
		}},
		{Name: "ValidateControlPlane", Fn: func(ctx context.Context) error {
			return uc.validateControlPlane(ctx, lab, nodes, links)
		}},
		{Name: "ValidateDataPlane", Fn: func(ctx context.Context) error {
			return uc.validateDataPlane(ctx, lab, nodes)
		}},
		// Redeploying wipes the server containers and everything the
		// agents were running; push the persisted program desired
		// state back onto the fresh agents.
		{Name: "RestorePrograms", Fn: func(ctx context.Context) error {
			return uc.programs.RestorePrograms(ctx, lab, nodes)
		}},
	}...)

	uc.ops.Run(op, steps, func(failed error) {
		if failed != nil {
			lab.Meta.Phase = model.PhaseFailed
			lab.Meta.LastError = &model.ResourceError{
				Code: "APPLY_FAILED", Message: failed.Error(), Time: time.Now().UTC(),
			}
		} else {
			lab.Meta.Generation = plan.NewGeneration
			lab.Meta.ObservedGeneration = plan.NewGeneration
			lab.Meta.Phase = model.PhaseRunning
			lab.Meta.LastError = nil
			plan.State = model.PlanApplied
			if err := uc.repo.UpdatePlan(plan); err != nil {
				uc.log.Error("update plan", "plan_id", plan.ID, "error", err)
			}

			for _, n := range nodes {
				n.Meta.Phase = model.PhaseRunning
				n.Meta.ObservedGeneration = plan.NewGeneration
				if err := uc.repo.UpdateNode(n); err != nil {
					uc.log.Error("update node", "node_id", n.Meta.ID, "error", err)
				}
			}
		}

		if err := uc.repo.UpdateLab(lab); err != nil {
			uc.log.Error("update lab", "lab_id", lab.Meta.ID, "error", err)
		}
	})

	return op, nil
}

// connectInternet attaches every external router to the WAN network
// after deploy. The compiler cannot emit this as exec commands: the
// WAN interface only exists once the container joins the network.
// Air-gapped labs and runtimes without the capability skip it.
func (uc *PlanUsecase) connectInternet(ctx context.Context, lab *model.Lab, nodes []*model.Node) error {
	if !lab.Spec.Topology.InternetAccess {
		return nil
	}

	for _, n := range nodes {
		if n.Spec.Role != model.RoleExternal {
			continue
		}

		err := uc.driver.ConnectInternet(ctx, lab.Meta.Name, n.Meta.Name)
		if errors.Is(err, runtime.ErrNotSupported) {
			uc.log.Info("internet attach skipped", "runtime", uc.driver.Name())

			return nil
		}

		if err != nil {
			return fmt.Errorf("connect %s to internet: %w", n.Meta.Name, err)
		}
	}

	return nil
}
