package biz

import (
	"log/slog"

	"github.com/ifantsai/dcnetlab/internal/model"
)

// OperationRepo abstracts the async-operation queries.
type OperationRepo interface {
	GetOperation(id string) (*model.Operation, error)
	ListOperations(labID string) ([]*model.Operation, error)
}

// OperationBroadcaster streams operation state changes per lab; the
// operation manager implements it.
type OperationBroadcaster interface {
	Subscribe(labID string) (<-chan *model.Operation, func())
}

// OperationUsecase serves the progress of asynchronous operations;
// creation and execution live in the plan and lab usecases.
type OperationUsecase struct {
	repo OperationRepo
	feed OperationBroadcaster
	log  *slog.Logger
}

// NewOperationUsecase wires the operation usecase.
func NewOperationUsecase(repo OperationRepo, feed OperationBroadcaster, log *slog.Logger) *OperationUsecase {
	return &OperationUsecase{repo: repo, feed: feed, log: log}
}

// GetOperation returns one operation by ID.
func (s *OperationUsecase) GetOperation(id string) (*model.Operation, error) {
	return s.repo.GetOperation(id)
}

// ListOperations returns the operations of a lab.
func (s *OperationUsecase) ListOperations(labID string) ([]*model.Operation, error) {
	return s.repo.ListOperations(labID)
}

// SubscribeOperations streams every state change of the lab's
// operations; cancel must be called to release the subscription.
func (s *OperationUsecase) SubscribeOperations(labID string) (<-chan *model.Operation, func()) {
	return s.feed.Subscribe(labID)
}
