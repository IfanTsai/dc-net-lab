package biz

import (
	"log/slog"

	"github.com/ifantsai/dcnetlab/internal/model"
)

// TopologyRepo abstracts the desired-state topology queries.
type TopologyRepo interface {
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
