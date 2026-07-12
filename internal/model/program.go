package model

import "time"

// Program desired and observed states. Exited is the terminal state
// of a oneshot program that ran to successful completion.
const (
	ProgramDesiredRunning = "Running"
	ProgramDesiredStopped = "Stopped"

	ProgramStateConfigured = "Configured"
	ProgramStateRunning    = "Running"
	ProgramStateStopped    = "Stopped"
	ProgramStateFailed     = "Failed"
	ProgramStateExited     = "Exited"
	ProgramStateUnknown    = "Unknown"
)

// Program is a supervised process on one lab server, managed by
// dcnetlab-node-agent; it runs the entrypoint of an installed
// Package version. Meta.Name doubles as the agent-side program name.
type Program struct {
	Meta   ResourceMeta  `json:"meta"`
	Spec   ProgramSpec   `json:"spec"`
	Status ProgramStatus `json:"status"`
}

// ProgramSpec is the desired state of a program. DesiredState and
// the package reference survive redeploys: the controller pushes
// them back onto the fresh agents after every apply. Entrypoint is
// denormalised from the referenced package version. Type and
// AutoStart follow systemd service semantics: a simple program is a
// long-running daemon, a oneshot runs to completion; an auto-start
// (enabled) program starts on every server boot and redeploy.
type ProgramSpec struct {
	LabID          string   `json:"labId"`
	ServerID       string   `json:"serverId"`
	ServerName     string   `json:"serverName"`
	PackageName    string   `json:"packageName"`
	PackageVersion string   `json:"packageVersion"`
	Entrypoint     string   `json:"entrypoint"`
	Args           []string `json:"args,omitempty"`
	RestartPolicy  string   `json:"restartPolicy"`
	Type           string   `json:"type"`
	AutoStart      bool     `json:"autoStart"`
	DesiredState   string   `json:"desiredState"`
}

// ProgramStatus is the agent-observed state of a program.
type ProgramStatus struct {
	State        string    `json:"state"`
	PID          int       `json:"pid,omitempty"`
	Restarts     int       `json:"restarts"`
	LastError    string    `json:"lastError,omitempty"`
	StartedAt    time.Time `json:"startedAt,omitzero"`
	LastObserved time.Time `json:"lastObserved,omitzero"`
}

// NodeProgram is the agent-reported live view of one program on a
// server, including programs created node-locally (in-container CLI)
// that the controller never declared; Managed marks the
// controller-known ones.
type NodeProgram struct {
	Name           string   `json:"name"`
	PackageName    string   `json:"packageName"`
	PackageVersion string   `json:"packageVersion"`
	Entrypoint     string   `json:"entrypoint"`
	Args           []string `json:"args,omitempty"`
	RestartPolicy  string   `json:"restartPolicy"`
	Type           string   `json:"type"`
	AutoStart      bool     `json:"autoStart"`
	State          string   `json:"state"`
	PID            int      `json:"pid,omitempty"`
	Restarts       int      `json:"restarts"`
	LastError      string   `json:"lastError,omitempty"`
	Managed        bool     `json:"managed"`
}

// NodeInventory is what is actually present on one server, live from
// its agent.
type NodeInventory struct {
	Packages []InstalledPackage `json:"packages"`
	Programs []NodeProgram      `json:"programs"`
}
