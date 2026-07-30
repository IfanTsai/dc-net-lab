package biz

import (
	"context"
	"fmt"
	"log/slog"
	"time"

	"github.com/ifantsai/dcnetlab/controller/internal/operation"
	"github.com/ifantsai/dcnetlab/internal/model"
	"github.com/ifantsai/dcnetlab/internal/runtime"
)

// PowerRepo abstracts the persistence the power usecase needs.
type PowerRepo interface {
	GetLab(id string) (*model.Lab, error)
	UpdateLab(lab *model.Lab) error
	ListNodes(labID string) ([]*model.Node, error)
	UpdateNode(n *model.Node) error
}

// PowerUsecase powers deployed labs and individual devices on and
// off by freezing containers in place (docker pause/unpause): the
// veth wiring and injected network config survive, so a stop is a
// silent outage and a start is an instant thaw. Real teardown and
// redeploy stay with DeleteLab / ApplyPlan.
type PowerUsecase struct {
	repo   PowerRepo
	ops    *operation.Manager
	driver runtime.Driver
	log    *slog.Logger
}

// NewPowerUsecase wires the power usecase.
func NewPowerUsecase(repo PowerRepo, ops *operation.Manager, driver runtime.Driver, log *slog.Logger) *PowerUsecase {
	return &PowerUsecase{repo: repo, ops: ops, driver: driver, log: log}
}

// StartLab powers on every device of a deployed lab asynchronously.
func (uc *PowerUsecase) StartLab(labID string) (*model.Operation, error) {
	return uc.powerLab(labID, true)
}

// StopLab powers off every device of a deployed lab asynchronously.
func (uc *PowerUsecase) StopLab(labID string) (*model.Operation, error) {
	return uc.powerLab(labID, false)
}

func (uc *PowerUsecase) powerLab(labID string, on bool) (*model.Operation, error) {
	lab, nodes, err := uc.deployedLab(labID)
	if err != nil {
		return nil, err
	}

	opType, stepName := model.OperationStopLab, "StopDevices"
	if on {
		opType, stepName = model.OperationStartLab, "StartDevices"
	}

	op, err := uc.ops.Create(lab.Meta.ID, opType, model.ResourceRef{Type: "lab", ID: labID})
	if err != nil {
		return nil, fmt.Errorf("create %s operation: %w", opType, err)
	}

	// Stopping freezes every container in place (docker pause), the
	// same semantics as a single device: the veth wiring and injected
	// network config survive, so start is an instant thaw. A real
	// teardown/redeploy stays with DeleteLab / ApplyPlan.
	//
	// Every node is passed down: the driver skips containers already
	// in the target state based on live docker status (recorded
	// phases can drift from reality after a partial failure), so a
	// lab stop after some devices were stopped individually
	// converges instead of failing the batch.
	names := make([]string, 0, len(nodes))
	for _, n := range nodes {
		names = append(names, n.Meta.Name)
	}

	steps := []operation.Step{
		{Name: stepName, Fn: func(ctx context.Context) error {
			if on {
				return uc.driver.StartNodes(ctx, lab.Meta.Name, names)
			}

			return uc.driver.StopNodes(ctx, lab.Meta.Name, names)
		}},
	}

	uc.ops.Run(op, steps, func(failed error) {
		if failed != nil {
			lab.Meta.Phase = model.PhaseFailed
			lab.Meta.LastError = &model.ResourceError{
				Code: "POWER_FAILED", Message: failed.Error(), Time: time.Now().UTC(),
			}
		} else {
			lab.Meta.Phase = model.PhaseStopped
			if on {
				lab.Meta.Phase = model.PhaseRunning
			}

			lab.Meta.LastError = nil
			uc.updateNodePhases(nodes, lab.Meta.Phase)
		}

		if err := uc.repo.UpdateLab(lab); err != nil {
			uc.log.Error("update lab", "lab_id", lab.Meta.ID, "error", err)
		}
	})

	return op, nil
}

// StartNode powers on one device and returns its updated state.
func (uc *PowerUsecase) StartNode(ctx context.Context, labID, nodeID string) (*model.Node, error) {
	return uc.powerNode(ctx, labID, nodeID, true)
}

// StopNode powers off one device and returns its updated state.
func (uc *PowerUsecase) StopNode(ctx context.Context, labID, nodeID string) (*model.Node, error) {
	return uc.powerNode(ctx, labID, nodeID, false)
}

func (uc *PowerUsecase) powerNode(ctx context.Context, labID, nodeID string, on bool) (*model.Node, error) {
	lab, nodes, err := uc.deployedLab(labID)
	if err != nil {
		return nil, err
	}

	var node *model.Node
	for _, n := range nodes {
		if n.Meta.ID == nodeID {
			node = n

			break
		}
	}

	if node == nil {
		return nil, fmt.Errorf("node %q: %w", nodeID, ErrNotFound)
	}

	if on {
		err = uc.driver.StartNodes(ctx, lab.Meta.Name, []string{node.Meta.Name})
	} else {
		err = uc.driver.StopNodes(ctx, lab.Meta.Name, []string{node.Meta.Name})
	}

	if err != nil {
		return nil, err
	}

	node.Meta.Phase = model.PhaseStopped
	if on {
		node.Meta.Phase = model.PhaseRunning
	}

	if err := uc.repo.UpdateNode(node); err != nil {
		return nil, fmt.Errorf("update node: %w", err)
	}

	uc.reconcileLabPhase(lab, nodes)

	return node, nil
}

// deployedLab loads a lab and its nodes, requiring a deployed
// generation: powering an undeployed lab has nothing to act on.
func (uc *PowerUsecase) deployedLab(labID string) (*model.Lab, []*model.Node, error) {
	lab, err := uc.repo.GetLab(labID)
	if err != nil {
		return nil, nil, fmt.Errorf("get lab: %w", err)
	}

	if lab.Meta.Generation == 0 {
		return nil, nil, fmt.Errorf("lab %q has no deployed generation; apply a plan first", lab.Meta.Name)
	}

	nodes, err := uc.repo.ListNodes(labID)
	if err != nil {
		return nil, nil, fmt.Errorf("list nodes: %w", err)
	}

	return lab, nodes, nil
}

func (uc *PowerUsecase) updateNodePhases(nodes []*model.Node, phase model.ResourcePhase) {
	for _, n := range nodes {
		n.Meta.Phase = phase
		if err := uc.repo.UpdateNode(n); err != nil {
			uc.log.Error("update node", "node_id", n.Meta.ID, "error", err)
		}
	}
}

// reconcileLabPhase derives the lab phase from its devices after a
// single-node power change: all up → Running, all down → Stopped,
// mixed → Degraded.
func (uc *PowerUsecase) reconcileLabPhase(lab *model.Lab, nodes []*model.Node) {
	up, down := 0, 0
	for _, n := range nodes {
		if n.Meta.Phase == model.PhaseStopped {
			down++
		} else {
			up++
		}
	}

	switch {
	case down == 0:
		lab.Meta.Phase = model.PhaseRunning
	case up == 0:
		lab.Meta.Phase = model.PhaseStopped
	default:
		lab.Meta.Phase = model.PhaseDegraded
	}

	if err := uc.repo.UpdateLab(lab); err != nil {
		uc.log.Error("update lab", "lab_id", lab.Meta.ID, "error", err)
	}
}
