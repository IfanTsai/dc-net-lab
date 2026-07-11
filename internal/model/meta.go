// Package model defines the core resource model of DCNetLab.
// The resource model is the single source of truth for desired state;
// Containerlab topologies, FRR configs and Linux state are compiled
// artifacts or observed state, never primary data.
package model

import "time"

// ResourcePhase is the unified lifecycle phase shared by all resources.
type ResourcePhase string

const (
	PhasePending  ResourcePhase = "Pending"
	PhasePlanning ResourcePhase = "Planning"
	PhaseApplying ResourcePhase = "Applying"
	PhaseRunning  ResourcePhase = "Running"
	PhaseDegraded ResourcePhase = "Degraded"
	PhaseFailed   ResourcePhase = "Failed"
	PhaseStopping ResourcePhase = "Stopping"
	PhaseStopped  ResourcePhase = "Stopped"
	PhaseDeleting ResourcePhase = "Deleting"
	PhaseDeleted  ResourcePhase = "Deleted"
)

// ResourceError is a structured error attached to a resource status.
type ResourceError struct {
	Code    string    `json:"code"`
	Message string    `json:"message"`
	Time    time.Time `json:"time"`
}

// ResourceMeta is the common metadata embedded in every resource.
type ResourceMeta struct {
	ID                 string         `json:"id"`
	Name               string         `json:"name"`
	Generation         int64          `json:"generation"`
	ObservedGeneration int64          `json:"observedGeneration"`
	Phase              ResourcePhase  `json:"phase"`
	LastError          *ResourceError `json:"lastError,omitempty"`
	CreatedAt          time.Time      `json:"createdAt"`
	UpdatedAt          time.Time      `json:"updatedAt"`
}
