package model

import "time"

// OperationType classifies an asynchronous operation.
type OperationType string

const (
	OperationApplyPlan  OperationType = "ApplyPlan"
	OperationDestroyLab OperationType = "DestroyLab"
	OperationStartLab   OperationType = "StartLab"
	OperationStopLab    OperationType = "StopLab"
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

// OperationStep is one step in an operation's execution.
type OperationStep struct {
	Name       string         `json:"name"`
	State      OperationState `json:"state"`
	Message    string         `json:"message,omitempty"`
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
