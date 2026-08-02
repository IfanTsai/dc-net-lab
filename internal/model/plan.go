package model

import "time"

// PlanOperationType is one kind of change inside a plan.
type PlanOperationType string

const (
	PlanCreateNode      PlanOperationType = "CreateNode"
	PlanUpdateNode      PlanOperationType = "UpdateNode"
	PlanDeleteNode      PlanOperationType = "DeleteNode"
	PlanCreateLink      PlanOperationType = "CreateLink"
	PlanDeleteLink      PlanOperationType = "DeleteLink"
	PlanAllocateAddress PlanOperationType = "AllocateAddress"
	PlanAllocateASN     PlanOperationType = "AllocateASN"
	PlanRenderConfig    PlanOperationType = "RenderConfiguration"
	PlanDeployTopology  PlanOperationType = "DeployTopology"
)

// PlanOperation is one intended change with a human-readable summary.
type PlanOperation struct {
	Type    PlanOperationType `json:"type"`
	Target  string            `json:"target"`
	Summary string            `json:"summary"`
}

// Allocation records one resource allocation the plan will consume.
type Allocation struct {
	Pool  string `json:"pool"`
	Value string `json:"value"`
	Owner string `json:"owner"`
}

// PlanWarning is a non-fatal issue detected while planning.
type PlanWarning struct {
	Code    string `json:"code"`
	Message string `json:"message"`
}

// PlanState is the lifecycle state of a plan.
type PlanState string

const (
	PlanPending PlanState = "Pending"
	PlanApplied PlanState = "Applied"
	PlanExpired PlanState = "Expired"
)

// Plan is a previewable change set computed from desired state.
type Plan struct {
	ID             string          `json:"id"`
	LabID          string          `json:"labId"`
	BaseGeneration int64           `json:"baseGeneration"`
	NewGeneration  int64           `json:"newGeneration"`
	State          PlanState       `json:"state"`
	Operations     []PlanOperation `json:"operations"`
	Allocations    []Allocation    `json:"allocations"`
	Warnings       []PlanWarning   `json:"warnings"`
	CreatedAt      time.Time       `json:"createdAt"`
}
