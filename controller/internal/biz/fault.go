package biz

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"regexp"
	"time"

	"github.com/ifantsai/dcnetlab/internal/model"
	"github.com/ifantsai/dcnetlab/internal/runtime"
)

// faultNameRE mirrors the resource naming convention used by traffic
// scenarios.
var faultNameRE = regexp.MustCompile(`^[a-z0-9][a-z0-9-]{0,62}$`)

// FaultRepo abstracts the persistence the fault usecase needs.
type FaultRepo interface {
	CreateFaultScenario(s *model.FaultScenario) error
	UpdateFaultScenario(s *model.FaultScenario) error
	DeleteFaultScenario(id string) error
	GetFaultScenario(id string) (*model.FaultScenario, error)
	ListFaultScenarios(labID string) ([]*model.FaultScenario, error)
	GetLab(id string) (*model.Lab, error)
	ListNodes(labID string) ([]*model.Node, error)
	ListLinks(labID string) ([]*model.Link, error)
}

// FaultUsecase manages FaultScenarios: controlled failures injected
// into a deployed lab and recovered on demand. Node faults reuse the
// power usecase (pause/unpause: a real container restart loses the
// containerlab veth wiring on WSL2/OrbStack, turning "recover" into
// "redeploy"); link faults go straight to the runtime driver
// (ip link / tc netem), the same exec path ConnectInternet uses.
//
// A target allows at most one applied scenario at a time, so recovery
// always restores the fixed baseline (interface up, no qdisc, node
// running) — outside of an applied fault that baseline is the only
// state the platform ever leaves a target in, making it equivalent to
// a pre-fault snapshot without storing one.
type FaultUsecase struct {
	repo   FaultRepo
	power  *PowerUsecase
	driver runtime.Driver
	log    *slog.Logger
}

// NewFaultUsecase wires the fault usecase.
func NewFaultUsecase(repo FaultRepo, power *PowerUsecase, driver runtime.Driver, log *slog.Logger) *FaultUsecase {
	return &FaultUsecase{repo: repo, power: power, driver: driver, log: log}
}

// CreateFaultScenario validates and persists one fault definition; it
// does not inject anything.
func (uc *FaultUsecase) CreateFaultScenario(labID, name string, spec model.FaultScenarioSpec) (*model.FaultScenario, error) {
	if !faultNameRE.MatchString(name) {
		return nil, fmt.Errorf("invalid scenario name %q (lowercase letters, digits, dashes, max 63 chars)", name)
	}

	if err := validateFaultType(spec.Type); err != nil {
		return nil, err
	}

	spec.LabID = labID
	if err := uc.resolveTarget(&spec); err != nil {
		return nil, err
	}

	if err := validateFaultShape(spec); err != nil {
		return nil, err
	}

	now := time.Now().UTC()
	s := &model.FaultScenario{
		Meta: model.ResourceMeta{
			ID: model.NewID("fault"), Name: name, CreatedAt: now, UpdatedAt: now,
		},
		Spec: spec,
	}

	if err := uc.repo.CreateFaultScenario(s); err != nil {
		return nil, fmt.Errorf("persist fault scenario: %w", err)
	}

	return s, nil
}

// validateFaultType rejects unknown fault types.
func validateFaultType(t string) error {
	switch t {
	case model.FaultNodeStop, model.FaultNodeRestart, model.FaultLinkDown, model.FaultInterfaceDown, model.FaultImpairment:
		return nil
	default:
		return fmt.Errorf("invalid fault type %q", t)
	}
}

// faultTargetsNode reports whether the fault type acts on a node (as
// opposed to a link).
func faultTargetsNode(t string) bool {
	return t == model.FaultNodeStop || t == model.FaultNodeRestart
}

// resolveTarget checks the target exists in the lab and denormalises
// its display name onto the spec.
func (uc *FaultUsecase) resolveTarget(spec *model.FaultScenarioSpec) error {
	if faultTargetsNode(spec.Type) {
		nodes, err := uc.repo.ListNodes(spec.LabID)
		if err != nil {
			return fmt.Errorf("list nodes: %w", err)
		}

		for _, n := range nodes {
			if n.Meta.ID == spec.Target.NodeID {
				spec.Target.Kind = model.FaultTargetNode
				spec.Target.NodeName = n.Meta.Name

				return nil
			}
		}

		return fmt.Errorf("node %q: %w", spec.Target.NodeID, ErrNotFound)
	}

	link, err := uc.linkByID(spec.LabID, spec.Target.LinkID)
	if err != nil {
		return err
	}

	spec.Target.Kind = model.FaultTargetLink
	spec.Target.LinkName = link.Meta.Name

	return nil
}

// linkByID resolves a link of the lab.
func (uc *FaultUsecase) linkByID(labID, linkID string) (*model.Link, error) {
	links, err := uc.repo.ListLinks(labID)
	if err != nil {
		return nil, fmt.Errorf("list links: %w", err)
	}

	for _, l := range links {
		if l.Meta.ID == linkID {
			return l, nil
		}
	}

	return nil, fmt.Errorf("link %q: %w", linkID, ErrNotFound)
}

// validateFaultShape enforces the per-type constraints: which target
// kind, side and impairment parameters each fault type accepts. It
// runs after resolveTarget, so the target kind is already set.
func validateFaultShape(spec model.FaultScenarioSpec) error {
	if spec.Type != model.FaultImpairment && spec.Impairment != nil {
		return fmt.Errorf("impairment parameters only apply to the impairment type")
	}

	switch spec.Type {
	case model.FaultNodeStop, model.FaultNodeRestart:
		if spec.Target.Side != "" {
			return fmt.Errorf("side does not apply to node faults")
		}

	case model.FaultLinkDown:
		if spec.Target.Side != "" && spec.Target.Side != model.FaultSideBoth {
			return fmt.Errorf("link-down always acts on both ends; leave side empty")
		}

	case model.FaultInterfaceDown:
		if spec.Target.Side != model.FaultSideA && spec.Target.Side != model.FaultSideB {
			return fmt.Errorf("interface-down needs side %q or %q", model.FaultSideA, model.FaultSideB)
		}

	case model.FaultImpairment:
		switch spec.Target.Side {
		case model.FaultSideA, model.FaultSideB, model.FaultSideBoth, "":
		default:
			return fmt.Errorf("invalid side %q (want a, b or both)", spec.Target.Side)
		}

		if err := validateImpairment(spec.Impairment); err != nil {
			return err
		}
	}

	return nil
}

// validateImpairment requires at least one effect and rejects
// out-of-range parameters.
func validateImpairment(imp *model.FaultImpairmentSpec) error {
	if imp == nil || (imp.DelayMs == 0 && imp.LossPercent == 0 && imp.RateKbit == 0) {
		return fmt.Errorf("impairment needs at least one of delayMs, lossPercent or rateKbit")
	}

	if imp.DelayMs < 0 || imp.JitterMs < 0 || imp.RateKbit < 0 {
		return fmt.Errorf("impairment parameters must be >= 0")
	}

	if imp.LossPercent < 0 || imp.LossPercent > 100 {
		return fmt.Errorf("lossPercent must be between 0 and 100")
	}

	if imp.JitterMs > 0 && imp.DelayMs == 0 {
		return fmt.Errorf("jitterMs needs delayMs")
	}

	return nil
}

// ApplyFaultScenario injects the fault. node-restart is a
// point-in-time event (stop, then start again) and reports applied =
// false afterwards; every other type stays applied until recovered.
func (uc *FaultUsecase) ApplyFaultScenario(ctx context.Context, labID, id string) (*model.FaultScenario, error) {
	s, err := uc.get(labID, id)
	if err != nil {
		return nil, err
	}

	if s.Status.Applied {
		return s, nil
	}

	if err := uc.checkTargetFree(s); err != nil {
		return nil, err
	}

	lab, err := uc.repo.GetLab(labID)
	if err != nil {
		return nil, fmt.Errorf("get lab: %w", err)
	}

	if lab.Meta.Generation == 0 {
		return nil, fmt.Errorf("lab %q has no deployed generation; apply a plan first", lab.Meta.Name)
	}

	applied, err := uc.inject(ctx, lab, s)

	s.Status.Applied = applied && err == nil
	s.Status.LastError = ""
	if err != nil {
		s.Status.LastError = err.Error()
	} else if s.Status.Applied {
		s.Status.AppliedAt = time.Now().UTC()
	}

	if uerr := uc.repo.UpdateFaultScenario(s); uerr != nil {
		return nil, fmt.Errorf("update fault scenario: %w", uerr)
	}

	if err != nil {
		return nil, err
	}

	return s, nil
}

// checkTargetFree enforces the single-applied-fault-per-target rule
// that keeps recovery snapshot-free: with at most one fault applied,
// the pre-fault state is always the fixed baseline.
func (uc *FaultUsecase) checkTargetFree(s *model.FaultScenario) error {
	scenarios, err := uc.repo.ListFaultScenarios(s.Spec.LabID)
	if err != nil {
		return fmt.Errorf("list fault scenarios: %w", err)
	}

	for _, other := range scenarios {
		if other.Meta.ID == s.Meta.ID || !other.Status.Applied {
			continue
		}

		sameNode := s.Spec.Target.NodeID != "" && other.Spec.Target.NodeID == s.Spec.Target.NodeID
		sameLink := s.Spec.Target.LinkID != "" && other.Spec.Target.LinkID == s.Spec.Target.LinkID
		if sameNode || sameLink {
			return fmt.Errorf("fault %q is already applied to this target; recover it first", other.Meta.Name)
		}
	}

	return nil
}

// inject performs the fault action. The returned bool reports whether
// the fault is a persistent state to be recovered later (false for
// node-restart, which self-heals immediately).
func (uc *FaultUsecase) inject(ctx context.Context, lab *model.Lab, s *model.FaultScenario) (bool, error) {
	switch s.Spec.Type {
	case model.FaultNodeStop:
		if _, err := uc.power.StopNode(ctx, lab.Meta.ID, s.Spec.Target.NodeID); err != nil {
			return false, fmt.Errorf("stop node: %w", err)
		}

		return true, nil

	case model.FaultNodeRestart:
		if _, err := uc.power.StopNode(ctx, lab.Meta.ID, s.Spec.Target.NodeID); err != nil {
			return false, fmt.Errorf("restart node (stop): %w", err)
		}

		if _, err := uc.power.StartNode(ctx, lab.Meta.ID, s.Spec.Target.NodeID); err != nil {
			return false, fmt.Errorf("restart node (start): %w", err)
		}

		return false, nil

	default:
		if err := uc.injectLinkFault(ctx, lab, s); err != nil {
			return false, err
		}

		return true, nil
	}
}

// faultEndpoints returns the link endpoints a fault acts on, honoring
// the side selection; link-down always acts on both ends.
func faultEndpoints(link *model.Link, faultType, side string) []model.LinkEndpoint {
	if faultType == model.FaultLinkDown {
		return []model.LinkEndpoint{link.Spec.EndpointA, link.Spec.EndpointB}
	}

	switch side {
	case model.FaultSideA:
		return []model.LinkEndpoint{link.Spec.EndpointA}
	case model.FaultSideB:
		return []model.LinkEndpoint{link.Spec.EndpointB}
	default:
		return []model.LinkEndpoint{link.Spec.EndpointA, link.Spec.EndpointB}
	}
}

// injectLinkFault applies a link-down/interface-down/impairment fault
// to the selected link endpoints. A failure on a later endpoint rolls
// back the earlier ones so a half-applied fault is not recorded as
// applied.
func (uc *FaultUsecase) injectLinkFault(ctx context.Context, lab *model.Lab, s *model.FaultScenario) error {
	link, err := uc.linkByID(lab.Meta.ID, s.Spec.Target.LinkID)
	if err != nil {
		return err
	}

	endpoints := faultEndpoints(link, s.Spec.Type, s.Spec.Target.Side)

	var done []model.LinkEndpoint
	for _, ep := range endpoints {
		if err := uc.applyEndpoint(ctx, lab.Meta.Name, s, ep); err != nil {
			uc.rollbackEndpoints(ctx, lab.Meta.Name, s, done)

			return err
		}

		done = append(done, ep)
	}

	return nil
}

// applyEndpoint injects the fault on one link endpoint.
func (uc *FaultUsecase) applyEndpoint(ctx context.Context, labName string, s *model.FaultScenario, ep model.LinkEndpoint) error {
	if s.Spec.Type == model.FaultImpairment {
		imp := s.Spec.Impairment

		return uc.driver.ApplyImpairment(ctx, labName, ep.NodeName, ep.Interface, runtime.Impairment{
			DelayMs:     imp.DelayMs,
			JitterMs:    imp.JitterMs,
			LossPercent: imp.LossPercent,
			RateKbit:    imp.RateKbit,
		})
	}

	return uc.driver.SetInterfaceState(ctx, labName, ep.NodeName, ep.Interface, false)
}

// recoverEndpoint restores the baseline on one link endpoint.
func (uc *FaultUsecase) recoverEndpoint(ctx context.Context, labName string, s *model.FaultScenario, ep model.LinkEndpoint) error {
	if s.Spec.Type == model.FaultImpairment {
		return uc.driver.ClearImpairment(ctx, labName, ep.NodeName, ep.Interface)
	}

	return uc.driver.SetInterfaceState(ctx, labName, ep.NodeName, ep.Interface, true)
}

// rollbackEndpoints best-effort restores endpoints that were already
// faulted before a later endpoint failed to apply.
func (uc *FaultUsecase) rollbackEndpoints(ctx context.Context, labName string, s *model.FaultScenario, done []model.LinkEndpoint) {
	for _, ep := range done {
		if err := uc.recoverEndpoint(ctx, labName, s, ep); err != nil {
			uc.log.Warn("fault: roll back endpoint after failed apply",
				"scenario", s.Meta.Name, "node", ep.NodeName, "iface", ep.Interface, "error", err)
		}
	}
}

// RecoverFaultScenario restores the target to its baseline. Endpoint
// recovery is best-effort across both ends: a failure on one end does
// not skip the other, and the scenario stays applied (with the error
// recorded) so recovery can be retried.
func (uc *FaultUsecase) RecoverFaultScenario(ctx context.Context, labID, id string) (*model.FaultScenario, error) {
	s, err := uc.get(labID, id)
	if err != nil {
		return nil, err
	}

	if !s.Status.Applied {
		if s.Spec.Type == model.FaultNodeRestart {
			return nil, fmt.Errorf("node-restart recovers by itself; there is nothing to recover")
		}

		return s, nil
	}

	lab, err := uc.repo.GetLab(labID)
	if err != nil {
		return nil, fmt.Errorf("get lab: %w", err)
	}

	err = uc.recover(ctx, lab, s)

	s.Status.Applied = err != nil
	s.Status.LastError = ""
	if err != nil {
		s.Status.LastError = err.Error()
	} else {
		s.Status.AppliedAt = time.Time{}
	}

	if uerr := uc.repo.UpdateFaultScenario(s); uerr != nil {
		return nil, fmt.Errorf("update fault scenario: %w", uerr)
	}

	if err != nil {
		return nil, err
	}

	return s, nil
}

// recover undoes the fault action.
func (uc *FaultUsecase) recover(ctx context.Context, lab *model.Lab, s *model.FaultScenario) error {
	if s.Spec.Type == model.FaultNodeStop {
		if _, err := uc.power.StartNode(ctx, lab.Meta.ID, s.Spec.Target.NodeID); err != nil {
			return fmt.Errorf("start node: %w", err)
		}

		return nil
	}

	link, err := uc.linkByID(lab.Meta.ID, s.Spec.Target.LinkID)
	if err != nil {
		return err
	}

	var errs []error
	for _, ep := range faultEndpoints(link, s.Spec.Type, s.Spec.Target.Side) {
		if err := uc.recoverEndpoint(ctx, lab.Meta.Name, s, ep); err != nil {
			errs = append(errs, err)
		}
	}

	return errors.Join(errs...)
}

// DeleteFaultScenario removes the scenario, recovering it first if it
// is still applied so no orphaned fault outlives its resource.
func (uc *FaultUsecase) DeleteFaultScenario(ctx context.Context, labID, id string) error {
	s, err := uc.get(labID, id)
	if err != nil {
		return err
	}

	if s.Status.Applied {
		if _, err := uc.RecoverFaultScenario(ctx, labID, id); err != nil {
			return fmt.Errorf("recover before delete: %w", err)
		}
	}

	return uc.repo.DeleteFaultScenario(id)
}

// ListFaultScenarios returns the fault scenarios of a lab.
func (uc *FaultUsecase) ListFaultScenarios(labID string) ([]*model.FaultScenario, error) {
	return uc.repo.ListFaultScenarios(labID)
}

// PruneForRemovedTargets deletes fault records whose target node or
// link a plan removed. Recovery is skipped deliberately: the faulted
// interfaces and containers died with the scale-in, so there is
// nothing left to restore — going through the regular delete would
// fail trying to recover into a container that no longer exists.
func (uc *FaultUsecase) PruneForRemovedTargets(labID string, removedNodes, removedLinks map[string]bool) error {
	scenarios, err := uc.repo.ListFaultScenarios(labID)
	if err != nil {
		return fmt.Errorf("list fault scenarios: %w", err)
	}

	for _, s := range scenarios {
		t := s.Spec.Target
		nodeGone := t.Kind == model.FaultTargetNode && removedNodes[t.NodeName]
		linkGone := t.Kind == model.FaultTargetLink && removedLinks[t.LinkName]
		if !nodeGone && !linkGone {
			continue
		}

		if err := uc.repo.DeleteFaultScenario(s.Meta.ID); err != nil {
			return fmt.Errorf("delete fault scenario %s: %w", s.Meta.Name, err)
		}

		uc.log.Info("pruned fault scenario of removed target", "scenario", s.Meta.Name)
	}

	return nil
}

// get loads a scenario of the lab, translating a lab mismatch into
// ErrNotFound like the traffic usecase does.
func (uc *FaultUsecase) get(labID, id string) (*model.FaultScenario, error) {
	s, err := uc.repo.GetFaultScenario(id)
	if err != nil {
		return nil, fmt.Errorf("get fault scenario: %w", err)
	}

	if s.Spec.LabID != labID {
		return nil, fmt.Errorf("fault scenario %q: %w", id, ErrNotFound)
	}

	return s, nil
}
