package biz

import (
	"fmt"
	"log/slog"

	"github.com/ifantsai/dcnetlab/controller/internal/compiler/frr"
	"github.com/ifantsai/dcnetlab/internal/model"
)

// TopologyRepo abstracts the desired-state topology queries.
type TopologyRepo interface {
	GetLab(id string) (*model.Lab, error)
	ListNodes(labID string) ([]*model.Node, error)
	ListLinks(labID string) ([]*model.Link, error)
	ListAllocations(labID string) ([]model.Allocation, error)
}

// TopologyUsecase serves the topology of a lab: nodes, links and the
// resource allocations they consume.
type TopologyUsecase struct {
	repo TopologyRepo
	log  *slog.Logger
}

// NewTopologyUsecase wires the topology usecase.
func NewTopologyUsecase(repo TopologyRepo, log *slog.Logger) *TopologyUsecase {
	return &TopologyUsecase{repo: repo, log: log}
}

// ListNodes returns the desired nodes of a lab.
func (uc *TopologyUsecase) ListNodes(labID string) ([]*model.Node, error) {
	return uc.repo.ListNodes(labID)
}

// ListLinks returns the desired links of a lab.
func (uc *TopologyUsecase) ListLinks(labID string) ([]*model.Link, error) {
	return uc.repo.ListLinks(labID)
}

// ListAllocations returns the resource allocations of a lab.
func (uc *TopologyUsecase) ListAllocations(labID string) ([]model.Allocation, error) {
	return uc.repo.ListAllocations(labID)
}

// GetNodeBGP returns one node's BGP configuration, derived by the
// same compiler that renders frr.conf so the view can never drift
// from the deployed config. Nodes that do not run BGP yield an empty
// config.
func (uc *TopologyUsecase) GetNodeBGP(labID, nodeID string) (*frr.RouterConfig, error) {
	lab, err := uc.repo.GetLab(labID)
	if err != nil {
		return nil, fmt.Errorf("get lab: %w", err)
	}

	nodes, err := uc.repo.ListNodes(labID)
	if err != nil {
		return nil, fmt.Errorf("list nodes: %w", err)
	}

	links, err := uc.repo.ListLinks(labID)
	if err != nil {
		return nil, fmt.Errorf("list links: %w", err)
	}

	found := false
	for _, n := range nodes {
		if n.Meta.ID == nodeID {
			found = true

			break
		}
	}

	if !found {
		return nil, fmt.Errorf("node %q: %w", nodeID, ErrNotFound)
	}

	cfgs, err := frr.BuildRouterConfigs(nodes, links, lab.Spec.Topology.InternetAccess)
	if err != nil {
		return nil, fmt.Errorf("build router configs: %w", err)
	}

	cfg, ok := cfgs[nodeID]
	if !ok {
		return &frr.RouterConfig{}, nil
	}

	return &cfg, nil
}
