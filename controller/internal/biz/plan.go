package biz

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"time"

	"github.com/ifantsai/dcnetlab/controller/internal/compiler"
	"github.com/ifantsai/dcnetlab/controller/internal/compiler/containerlab"
	"github.com/ifantsai/dcnetlab/controller/internal/conf"
	"github.com/ifantsai/dcnetlab/controller/internal/operation"
	"github.com/ifantsai/dcnetlab/controller/internal/topology"
	"github.com/ifantsai/dcnetlab/internal/model"
	"github.com/ifantsai/dcnetlab/internal/runtime"
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
	ListPrograms(labID string) ([]*model.Program, error)
	UpdateNode(n *model.Node) error
	CreatePlan(p *model.Plan) error
	UpdatePlan(p *model.Plan) error
	GetPlan(id string) (*model.Plan, error)
	SaveGeneration(labID string, generation int64, snap *DesiredStateSnapshot) error
	GetGeneration(labID string, generation int64) (*DesiredStateSnapshot, error)
	ListGenerations(labID string) ([]GenerationInfo, error)
}

// GenerationInfo summarises one retained generation snapshot — the
// rollback targets a lab can return to.
type GenerationInfo struct {
	Generation int64
	CreatedAt  time.Time
	NodeCount  int
	LinkCount  int
}

// PlanUsecase owns the declarative change flow: compute a previewable
// plan, then apply it (compile artifacts, deploy, validate).
type PlanUsecase struct {
	repo     PlanRepo
	ops      *operation.Manager
	driver   runtime.Driver
	programs *ProgramUsecase
	traffic  *TrafficUsecase
	faults   *FaultUsecase
	dataDir  string
	log      *slog.Logger
}

// NewPlanUsecase wires the plan usecase.
func NewPlanUsecase(repo PlanRepo, ops *operation.Manager, driver runtime.Driver,
	programs *ProgramUsecase, traffic *TrafficUsecase, faults *FaultUsecase,
	c *conf.Data, log *slog.Logger) *PlanUsecase {
	return &PlanUsecase{
		repo: repo, ops: ops, driver: driver,
		programs: programs, traffic: traffic, faults: faults,
		dataDir: c.Dir, log: log,
	}
}

// CreatePlan rebuilds the desired topology for the lab, diffs it
// against the deployed generation, persists it as desired state and
// returns a previewable plan. The deployed base pins every piece of
// identity the spec does not carry (rack numbers, addresses, ASNs,
// interface indices), so unchanged nodes and links rebuild
// bit-for-bit and the plan lists only actual changes.
func (uc *PlanUsecase) CreatePlan(labID string) (*model.Plan, error) {
	lab, err := uc.repo.GetLab(labID)
	if err != nil {
		return nil, fmt.Errorf("get lab: %w", err)
	}

	var base *topology.Base
	if lab.Meta.Generation > 0 {
		snap, err := uc.repo.GetGeneration(labID, lab.Meta.Generation)
		if err != nil {
			return nil, fmt.Errorf("load deployed generation %d: %w", lab.Meta.Generation, err)
		}

		base = &topology.Base{Nodes: snap.Nodes, Links: snap.Links}
	}

	builder, err := topology.NewBuilder(lab.Meta.ID, lab.Spec, base)
	if err != nil {
		return nil, fmt.Errorf("create topology builder: %w", err)
	}

	res, err := builder.Build(lab.Spec.Topology)
	if err != nil {
		return nil, fmt.Errorf("build topology: %w", err)
	}

	curNodes, err := uc.repo.ListNodes(labID)
	if err != nil {
		return nil, fmt.Errorf("list nodes: %w", err)
	}

	curLinks, err := uc.repo.ListLinks(labID)
	if err != nil {
		return nil, fmt.Errorf("list links: %w", err)
	}

	carryOverIdentity(res, curNodes, curLinks)

	programs, err := uc.repo.ListPrograms(labID)
	if err != nil {
		return nil, fmt.Errorf("list programs: %w", err)
	}

	ops, warnings := planChanges(base, res, programs)

	var newAllocs []model.Allocation
	for _, a := range res.Allocations {
		if builder.IsNewAllocation(a) {
			newAllocs = append(newAllocs, a)
		}
	}

	plan := &model.Plan{
		ID:             model.NewID("plan"),
		LabID:          lab.Meta.ID,
		BaseGeneration: lab.Meta.Generation,
		NewGeneration:  lab.Meta.Generation + 1,
		State:          model.PlanPending,
		Operations:     ops,
		Allocations:    newAllocs,
		Warnings:       warnings,
		CreatedAt:      time.Now().UTC(),
	}

	plan.Operations = append(plan.Operations,
		model.PlanOperation{Type: model.PlanRenderConfig, Target: lab.Meta.Name, Summary: "render FRR and Containerlab artifacts"},
		model.PlanOperation{Type: model.PlanDeployTopology, Target: lab.Meta.Name, Summary: "deploy topology via " + uc.driver.Name()},
	)

	// Desired state is persisted at plan time so the plan is exactly
	// what apply will deploy; the persisted allocation set is the full
	// one, not the diff — it seeds the next rebuild's restore.
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

// ListGenerations returns the retained generation snapshots of a lab,
// newest first.
func (uc *PlanUsecase) ListGenerations(labID string) ([]GenerationInfo, error) {
	return uc.repo.ListGenerations(labID)
}

// CreateRollbackPlan creates a plan whose desired state is a retained
// generation snapshot. It goes through the same diff preview and
// (incremental) apply as any other plan; generation numbers keep
// increasing — a rollback is a roll-forward to old content. Programs
// pruned by an earlier scale-in are not resurrected: the rollback
// restores the network, not the workloads that lived on it.
func (uc *PlanUsecase) CreateRollbackPlan(labID string, generation int64) (*model.Plan, error) {
	lab, err := uc.repo.GetLab(labID)
	if err != nil {
		return nil, fmt.Errorf("get lab: %w", err)
	}

	if lab.Meta.Generation == 0 {
		return nil, fmt.Errorf("lab %q has no deployed generation to roll back from", lab.Meta.Name)
	}

	if generation == lab.Meta.Generation {
		return nil, fmt.Errorf("generation %d is already deployed", generation)
	}

	target, err := uc.repo.GetGeneration(labID, generation)
	if err != nil {
		return nil, fmt.Errorf("load generation %d: %w", generation, err)
	}

	baseSnap, err := uc.repo.GetGeneration(labID, lab.Meta.Generation)
	if err != nil {
		return nil, fmt.Errorf("load deployed generation %d: %w", lab.Meta.Generation, err)
	}

	base := &topology.Base{Nodes: baseSnap.Nodes, Links: baseSnap.Links}
	res := &topology.Result{
		Nodes:       target.Nodes,
		Links:       target.Links,
		Allocations: topology.DeriveAllocations(target.Nodes, target.Links),
	}

	curNodes, err := uc.repo.ListNodes(labID)
	if err != nil {
		return nil, fmt.Errorf("list nodes: %w", err)
	}

	curLinks, err := uc.repo.ListLinks(labID)
	if err != nil {
		return nil, fmt.Errorf("list links: %w", err)
	}

	carryOverIdentity(res, curNodes, curLinks)
	resetReAddedResources(res, curNodes, curLinks)

	programs, err := uc.repo.ListPrograms(labID)
	if err != nil {
		return nil, fmt.Errorf("list programs: %w", err)
	}

	ops, warnings := planChanges(base, res, programs)

	baseHeld := make(map[string]bool)
	for _, a := range topology.DeriveAllocations(base.Nodes, base.Links) {
		baseHeld[a.Pool+"|"+a.Owner] = true
	}

	var newAllocs []model.Allocation
	for _, a := range res.Allocations {
		if !baseHeld[a.Pool+"|"+a.Owner] {
			newAllocs = append(newAllocs, a)
		}
	}

	plan := &model.Plan{
		ID:             model.NewID("plan"),
		LabID:          lab.Meta.ID,
		BaseGeneration: lab.Meta.Generation,
		NewGeneration:  lab.Meta.Generation + 1,
		State:          model.PlanPending,
		Operations:     ops,
		Allocations:    newAllocs,
		Warnings:       warnings,
		CreatedAt:      time.Now().UTC(),
	}

	plan.Operations = append(plan.Operations,
		model.PlanOperation{Type: model.PlanRenderConfig, Target: lab.Meta.Name, Summary: "render FRR and Containerlab artifacts"},
		model.PlanOperation{Type: model.PlanDeployTopology, Target: lab.Meta.Name, Summary: "deploy topology via " + uc.driver.Name()},
	)

	if err := uc.repo.ReplaceTopology(lab.Meta.ID, res.Nodes, res.Links, res.Allocations); err != nil {
		return nil, fmt.Errorf("persist desired topology: %w", err)
	}

	if err := uc.repo.CreatePlan(plan); err != nil {
		return nil, fmt.Errorf("persist plan: %w", err)
	}

	// The spec must follow the snapshot so the next regular plan
	// rebuilds the rolled-back shape instead of re-applying the
	// current one.
	lab.Spec = target.Lab.Spec
	lab.Meta.Phase = model.PhasePlanning
	if err := uc.repo.UpdateLab(lab); err != nil {
		return nil, fmt.Errorf("update lab: %w", err)
	}

	return plan, nil
}

// resetReAddedResources clears the snapshot-era runtime identity of
// nodes and links a rollback brings back: their containers do not
// exist yet, so they start Pending like any other planned creation.
func resetReAddedResources(res *topology.Result, curNodes []*model.Node, curLinks []*model.Link) {
	nodeExists := make(map[string]bool, len(curNodes))
	for _, n := range curNodes {
		nodeExists[n.Meta.Name] = true
	}

	linkExists := make(map[string]bool, len(curLinks))
	for _, l := range curLinks {
		linkExists[l.Meta.Name] = true
	}

	for _, n := range res.Nodes {
		if !nodeExists[n.Meta.Name] {
			n.Meta.Phase = model.PhasePending
			n.Meta.ObservedGeneration = 0
			n.Status = model.NodeStatus{}
		}
	}

	for _, l := range res.Links {
		if !linkExists[l.Meta.Name] {
			l.Meta.Phase = model.PhasePending
			l.Meta.ObservedGeneration = 0
			l.Status = model.LinkStatus{}
		}
	}
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

	// A first apply deploys from scratch; every later one is applied
	// incrementally against the deployed generation, so unchanged
	// containers are never restarted.
	var baseSnap *DesiredStateSnapshot
	if plan.BaseGeneration > 0 {
		baseSnap, err = uc.repo.GetGeneration(lab.Meta.ID, plan.BaseGeneration)
		if err != nil {
			return nil, fmt.Errorf("load deployed generation %d: %w", plan.BaseGeneration, err)
		}
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
	var artifact *compiler.Artifact

	// All node images are dcnetlab builds (capture and node-agent bake
	// in); verify them up front so a missing build fails with a clear
	// hint instead of a mid-deploy pull error.
	images := []string{opts.FRRImage, opts.ServerImage}
	if lab.Spec.Topology.InternetAccess {
		images = append(images, opts.EdgeImage)
	}

	steps := []operation.Step{{Name: "EnsureImages", Fn: func(ctx context.Context) error {
		for _, image := range images {
			if err := uc.driver.EnsureImage(ctx, image); err != nil {
				return fmt.Errorf("node image missing, build it with `make images`: %w", err)
			}
		}

		return nil
	}}}

	steps = append(steps, []operation.Step{
		{Name: "CompileArtifacts", Fn: func(ctx context.Context) error {
			a, err := compiler.Compile(lab, nodes, links, opts)
			artifact = a

			return err
		}},
		{Name: "WriteArtifacts", Fn: func(ctx context.Context) error {
			return compiler.WriteArtifact(artifact, genDir)
		}},
		{Name: "SaveGeneration", Fn: func(ctx context.Context) error {
			return uc.repo.SaveGeneration(lab.Meta.ID, plan.NewGeneration,
				&DesiredStateSnapshot{Lab: lab, Nodes: nodes, Links: links})
		}},
		// Weights pace the progress bar by measured wall-clock share: a
		// full deploy dwarfs the bookkeeping steps, an incremental one
		// less so, and the two validators dominate the tail.
		{Name: "DeployTopology", Weight: deployWeight(baseSnap == nil), Fn: func(ctx context.Context) error {
			if baseSnap == nil {
				return uc.driver.Deploy(ctx, genDir)
			}

			base := &topology.Base{Nodes: baseSnap.Nodes, Links: baseSnap.Links}
			inc, err := buildIncrement(lab.Meta.Name, base, nodes, links,
				generationDir(uc.dataDir, lab.Meta.ID, plan.BaseGeneration), genDir)
			if err != nil {
				return err
			}

			if err := writeIncrement(inc, genDir); err != nil {
				return err
			}

			if inc.Empty() {
				return nil
			}

			return uc.driver.DeployIncrement(ctx, genDir)
		}},
		// A scale-in leaves records pointing at resources that no
		// longer exist: programs on removed servers, traffic scenarios
		// that lost an endpoint, faults on removed targets. Clean them
		// up before program restore so nothing tries to reach the dead
		// agents.
		{Name: "PruneRemovedResources", Fn: func(ctx context.Context) error {
			return uc.pruneRemovedResources(ctx, lab, baseSnap, nodes, links)
		}},
		{Name: "ConnectInternet", Fn: func(ctx context.Context) error {
			return uc.connectInternet(ctx, lab, nodes)
		}},
		{Name: "ValidateControlPlane", Weight: 4, Fn: func(ctx context.Context) error {
			return uc.validateControlPlane(ctx, lab, nodes, links)
		}},
		{Name: "ValidateDataPlane", Weight: 5, Fn: func(ctx context.Context) error {
			return uc.validateDataPlane(ctx, lab, nodes)
		}},
		// Fresh server containers boot without the capture tool (it is
		// not baked into their image); deliver it through the package
		// repository so server-side capture works like on switches. An
		// incremental apply scopes both this and the program restore
		// to the servers it created: the surviving agents kept their
		// packages and running programs (whose reinstall the agent
		// would refuse anyway).
		{Name: "InstallCaptureTool", Fn: func(ctx context.Context) error {
			return uc.programs.InstallCaptureTool(ctx, lab, restoreScope(baseSnap, nodes))
		}},
		// A full redeploy wipes the server containers and everything
		// the agents were running; push the persisted program desired
		// state back onto the fresh agents.
		{Name: "RestorePrograms", Fn: func(ctx context.Context) error {
			return uc.programs.RestorePrograms(ctx, lab, restoreScope(baseSnap, nodes))
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

// deployWeight sizes the DeployTopology step for the progress bar: a
// full containerlab deploy recreates every container, an incremental
// one only touches the delta.
func deployWeight(full bool) int {
	if full {
		return 12
	}

	return 5
}

// RepairLab re-attaches simulated interfaces the runtime lost outside
// the platform's control (typically a host Docker daemon restart
// dropping the veth pairs containerlab wired in — see docs/progress.md
// "关键经验记录" #3). It re-runs the deploy against the lab's current
// generation artifact — already-running containers keep their PID,
// uptime and programs; containerlab just re-attaches what is missing
// (idempotent, verified in the real environment: FRR/zebra picks the
// reappeared interfaces straight back up over netlink, no config
// re-injection needed). Unlike ApplyPlan this never changes the
// generation, so resource IDs are stable across a repair.
func (uc *PlanUsecase) RepairLab(labID string) (*model.Operation, error) {
	lab, err := uc.repo.GetLab(labID)
	if err != nil {
		return nil, fmt.Errorf("get lab: %w", err)
	}

	if lab.Meta.Generation == 0 {
		return nil, fmt.Errorf("lab %q has no deployed generation; apply a plan first", lab.Meta.Name)
	}

	nodes, err := uc.repo.ListNodes(labID)
	if err != nil {
		return nil, fmt.Errorf("list nodes: %w", err)
	}

	links, err := uc.repo.ListLinks(labID)
	if err != nil {
		return nil, fmt.Errorf("list links: %w", err)
	}

	op, err := uc.ops.Create(lab.Meta.ID, model.OperationRepairLab, model.ResourceRef{Type: "lab", ID: labID})
	if err != nil {
		return nil, fmt.Errorf("create repair operation: %w", err)
	}

	genDir := generationDir(uc.dataDir, lab.Meta.ID, lab.Meta.Generation)
	steps := []operation.Step{
		{Name: "DeployTopology", Weight: deployWeight(true), Fn: func(ctx context.Context) error {
			return uc.driver.Deploy(ctx, genDir)
		}},
		{Name: "ConnectInternet", Fn: func(ctx context.Context) error {
			return uc.connectInternet(ctx, lab, nodes)
		}},
		{Name: "ValidateControlPlane", Weight: 4, Fn: func(ctx context.Context) error {
			return uc.validateControlPlane(ctx, lab, nodes, links)
		}},
		{Name: "ValidateDataPlane", Weight: 5, Fn: func(ctx context.Context) error {
			return uc.validateDataPlane(ctx, lab, nodes)
		}},
	}

	uc.ops.Run(op, steps, func(failed error) {
		lab.Meta.Phase = model.PhaseRunning
		lab.Meta.LastError = nil
		if failed != nil {
			lab.Meta.Phase = model.PhaseFailed
			lab.Meta.LastError = &model.ResourceError{
				Code: "REPAIR_FAILED", Message: failed.Error(), Time: time.Now().UTC(),
			}
		}

		if err := uc.repo.UpdateLab(lab); err != nil {
			uc.log.Error("update lab", "lab_id", lab.Meta.ID, "error", err)
		}
	})

	return op, nil
}

// pruneRemovedResources drops the records that reference nodes or
// links a plan removed. Traffic scenarios go first through their
// regular delete (stopping the surviving endpoint's program on its
// live agent), then the remaining programs of removed servers and the
// faults on removed targets are cleaned up record-only.
func (uc *PlanUsecase) pruneRemovedResources(ctx context.Context, lab *model.Lab,
	baseSnap *DesiredStateSnapshot, nodes []*model.Node, links []*model.Link) error {
	if baseSnap == nil {
		return nil
	}

	nodeKept := make(map[string]bool, len(nodes))
	for _, n := range nodes {
		nodeKept[n.Meta.Name] = true
	}

	linkKept := make(map[string]bool, len(links))
	for _, l := range links {
		linkKept[l.Meta.Name] = true
	}

	removedNodes := make(map[string]bool)
	for _, n := range baseSnap.Nodes {
		if !nodeKept[n.Meta.Name] {
			removedNodes[n.Meta.Name] = true
		}
	}

	removedLinks := make(map[string]bool)
	for _, l := range baseSnap.Links {
		if !linkKept[l.Meta.Name] {
			removedLinks[l.Meta.Name] = true
		}
	}

	if len(removedNodes) == 0 && len(removedLinks) == 0 {
		return nil
	}

	if err := uc.traffic.PruneForRemovedServers(ctx, lab.Meta.ID, removedNodes); err != nil {
		return err
	}

	if err := uc.programs.PruneServerPrograms(lab.Meta.ID, removedNodes); err != nil {
		return err
	}

	return uc.faults.PruneForRemovedTargets(lab.Meta.ID, removedNodes, removedLinks)
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
