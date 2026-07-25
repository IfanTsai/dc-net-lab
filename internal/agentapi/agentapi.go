// Package agentapi holds the wire-level vocabulary shared by the
// controller and dcnetlab-node-agent: the agent port and the string
// enums travelling over the ServerAgent gRPC API. The agent
// implementation itself lives under serverapps/ and is not importable
// from the controller.
package agentapi

// DefaultPort is the agent's gRPC port on the management network.
const DefaultPort = 50061

// DefaultMetricsPort is the agent's Prometheus /metrics port,
// node-exporter's conventional 9100.
const DefaultMetricsPort = 9100

// DefaultRepoPort is the controller's package repository port; lab
// servers reach it via their management gateway.
const DefaultRepoPort = 50062

// Program states reported by the agent. Exited is the terminal state
// of a oneshot program that ran to successful completion (systemd:
// inactive/dead after a Type=oneshot unit).
const (
	StateConfigured = "Configured"
	StateRunning    = "Running"
	StateStopped    = "Stopped"
	StateFailed     = "Failed"
	StateExited     = "Exited"
)

// Program restart policies.
const (
	RestartNever     = "Never"
	RestartOnFailure = "OnFailure"
	RestartAlways    = "Always"
)

// Program types, following systemd service Type semantics: a simple
// program is a long-running daemon; a oneshot program runs to
// completion (Always restart is rejected for it, like systemd).
const (
	TypeSimple  = "simple"
	TypeOneshot = "oneshot"
)

// Liveness check types (systemd health-check / k8s probe semantics).
// Process is alive as long as the supervised process exists; tcp
// dials a local port; http issues a GET and expects a 2xx/3xx status.
const (
	CheckProcess = "process"
	CheckTCP     = "tcp"
	CheckHTTP    = "http"
)

// Program liveness states reported by the agent. Unknown means no
// liveness check is configured or none has run yet; Healthy and
// Unhealthy are the last probe verdict of a running program.
const (
	HealthUnknown   = "Unknown"
	HealthHealthy   = "Healthy"
	HealthUnhealthy = "Unhealthy"
)

// In-container paths of the bind-mounted agent binaries; the compiler
// mounts them there and the start script links them into PATH.
const (
	AgentMount = "/opt/dcnetlab/bin/node-agent"
	CLIMount   = "/opt/dcnetlab/bin/node-cli"
)

// StartAgentScript boots node-agent inside a server container (PATH
// symlinks, then the daemon in the background). It runs under `sh -c`
// both at deploy time (containerlab exec) and when the observer
// revives a dead agent, so the two paths cannot drift.
const StartAgentScript = `mkdir -p /opt/dcnetlab/run && ` +
	`ln -sf ` + AgentMount + ` /usr/local/bin/node-agent && ` +
	`ln -sf ` + CLIMount + ` /usr/local/bin/node-cli && ` +
	`ln -sf ` + CLIMount + ` /usr/local/bin/pkg && ` +
	`ln -sf ` + CLIMount + ` /usr/local/bin/program && ` +
	`nohup ` + AgentMount + ` >>/opt/dcnetlab/run/agent.log 2>&1 &`
