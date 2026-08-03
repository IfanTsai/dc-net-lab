package model

import "time"

// OperationType classifies an asynchronous operation.
type OperationType string

const (
	OperationApplyPlan  OperationType = "ApplyPlan"
	OperationDestroyLab OperationType = "DestroyLab"
	OperationStartLab   OperationType = "StartLab"
	OperationStopLab    OperationType = "StopLab"
	OperationRepairLab  OperationType = "RepairLab"
)

// OperationState is the lifecycle state of an operation.
type OperationState string

const (
	OperationQueued    OperationState = "Queued"
	OperationRunning   OperationState = "Running"
	OperationSucceeded OperationState = "Succeeded"
	OperationFailed    OperationState = "Failed"
	OperationCancelled OperationState = "Cancelled"
)

// OperationStep is one step in an operation's execution. Weight is
// the step's relative share of the operation's progress bar.
type OperationStep struct {
	Name       string         `json:"name"`
	State      OperationState `json:"state"`
	Message    string         `json:"message,omitempty"`
	Weight     int            `json:"weight,omitempty"`
	StartedAt  *time.Time     `json:"startedAt,omitempty"`
	FinishedAt *time.Time     `json:"finishedAt,omitempty"`
}

// OperationError is a structured operation failure.
type OperationError struct {
	Code    string `json:"code"`
	Message string `json:"message"`
}

// ResourceRef points at the resource an operation acts on.
type ResourceRef struct {
	Type string `json:"type"`
	ID   string `json:"id"`
}

// Operation tracks one asynchronous change. Every write API returns
// an operation ID; the UI follows progress via polling or WebSocket.
type Operation struct {
	ID        string          `json:"id"`
	LabID     string          `json:"labId"`
	Type      OperationType   `json:"type"`
	Resource  ResourceRef     `json:"resource"`
	State     OperationState  `json:"state"`
	Progress  int             `json:"progress"`
	Steps     []OperationStep `json:"steps"`
	Error     *OperationError `json:"error,omitempty"`
	CreatedAt time.Time       `json:"createdAt"`
	UpdatedAt time.Time       `json:"updatedAt"`
}

// Clone returns a deep copy the caller may hand to another goroutine
// while the executor keeps mutating the original.
func (op *Operation) Clone() *Operation {
	cp := *op

	cp.Steps = make([]OperationStep, len(op.Steps))
	for i, st := range op.Steps {
		if st.StartedAt != nil {
			t := *st.StartedAt
			st.StartedAt = &t
		}

		if st.FinishedAt != nil {
			t := *st.FinishedAt
			st.FinishedAt = &t
		}

		cp.Steps[i] = st
	}

	if op.Error != nil {
		e := *op.Error
		cp.Error = &e
	}

	return &cp
}
