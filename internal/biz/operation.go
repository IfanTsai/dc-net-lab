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

// OperationUsecase serves the progress of asynchronous operations;
// creation and execution live in the plan and lab usecases.
type OperationUsecase struct {
	repo OperationRepo
	log  *slog.Logger
}

// NewOperationUsecase wires the operation usecase.
func NewOperationUsecase(repo OperationRepo, log *slog.Logger) *OperationUsecase {
	return &OperationUsecase{repo: repo, log: log}
}

// GetOperation returns one operation by ID.
func (s *OperationUsecase) GetOperation(id string) (*model.Operation, error) {
	return s.repo.GetOperation(id)
}

// ListOperations returns the operations of a lab.
func (s *OperationUsecase) ListOperations(labID string) ([]*model.Operation, error) {
	return s.repo.ListOperations(labID)
}
