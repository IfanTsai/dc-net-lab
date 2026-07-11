package biz

import (
	"context"
	"fmt"
	"log/slog"

	"github.com/ifantsai/dcnetlab/internal/model"
	"github.com/ifantsai/dcnetlab/internal/runtime"
)

// TerminalUsecase opens interactive shells inside deployed lab nodes
// for the web console.
type TerminalUsecase struct {
	labs   LabRepo
	nodes  TopologyRepo
	driver runtime.Driver
	log    *slog.Logger
}

// NewTerminalUsecase wires the terminal usecase.
func NewTerminalUsecase(labs LabRepo, nodes TopologyRepo, driver runtime.Driver, log *slog.Logger) *TerminalUsecase {
	return &TerminalUsecase{labs: labs, nodes: nodes, driver: driver, log: log}
}

// OpenNodeTerminal starts an interactive shell in one node of a
// deployed lab: vtysh on network devices, bash on servers. The caller
// owns the returned session and must Close it.
func (uc *TerminalUsecase) OpenNodeTerminal(ctx context.Context, labID, nodeID string) (runtime.TerminalSession, error) {
	lab, err := uc.labs.GetLab(labID)
	if err != nil {
		return nil, fmt.Errorf("get lab: %w", err)
	}

	if lab.Meta.Generation == 0 {
		return nil, fmt.Errorf("lab %q has no deployed generation; apply a plan first", lab.Meta.Name)
	}

	nodes, err := uc.nodes.ListNodes(labID)
	if err != nil {
		return nil, fmt.Errorf("list nodes: %w", err)
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

	session, err := uc.driver.OpenTerminal(ctx, lab.Meta.Name, node.Meta.Name, terminalCommand(node.Spec))
	if err != nil {
		return nil, err
	}

	uc.log.Info("terminal opened", "lab", lab.Meta.Name, "node", node.Meta.Name)

	return session, nil
}

// terminalCommand picks the shell for a node: network devices land in
// the FRR CLI, servers in a plain shell (vtysh stays reachable there).
func terminalCommand(spec model.NodeSpec) []string {
	if spec.Role == model.RoleServer || !spec.IsRouter() {
		return []string{"bash"}
	}

	return []string{"vtysh"}
}
