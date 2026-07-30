// Package runtime defines the contract between the control plane and
// the machines running lab containers: the Driver interface with its
// session types, shared constants and the noop fallback. The real
// containerlab implementation lives in the data plane (agent);
// the controller reaches it through a gRPC client implementing the
// same interface.
package runtime

import (
	"context"
	"errors"
	"io"
)

// ErrNotSupported is returned by drivers that cannot perform an
// operation (e.g. exec on the noop driver). Callers treat it as
// "skip", not as a failure.
var ErrNotSupported = errors.New("operation not supported by this runtime driver")

// ErrUnavailable marks failures of the runtime itself — the docker
// daemon unreachable or its socket denying access — as opposed to a
// container-level error. Polling cannot heal these, so callers should
// fail fast instead of retrying until their deadline.
var ErrUnavailable = errors.New("container runtime unavailable")

// Driver deploys and destroys lab topologies.
type Driver interface {
	// Name identifies the backend ("containerlab", "noop").
	Name() string
	// Deploy brings up the topology whose files live under dir.
	Deploy(ctx context.Context, dir string) error
	// Destroy tears down the topology whose files live under dir.
	Destroy(ctx context.Context, dir string) error
	// Exec runs a command inside a deployed node's container and
	// returns its combined output.
	Exec(ctx context.Context, labName, nodeName string, cmd []string) ([]byte, error)
	// OpenTerminal starts an interactive command inside a deployed
	// node's container, attached to a pseudo-terminal.
	OpenTerminal(ctx context.Context, labName, nodeName string, cmd []string) (TerminalSession, error)
	// ExecStream starts a long-lived non-interactive command inside a
	// deployed node's container, streaming its stdout; closing the
	// session closes the command's stdin, which tools like capture
	// take as their stop signal.
	ExecStream(ctx context.Context, labName, nodeName string, cmd []string) (ExecSession, error)
	// NodeStates reports the live container state ("running",
	// "paused", "exited", ... or "missing") of each named node.
	NodeStates(ctx context.Context, labName string, nodeNames []string) (map[string]string, error)
	// NodeAddress returns the management-network IP of a deployed
	// node's container, used to reach in-container agents.
	NodeAddress(ctx context.Context, labName, nodeName string) (string, error)
	// NodeGateway returns the host-side gateway IP of a deployed
	// node's management network — the address under which the node
	// reaches services on the host (the package repository).
	NodeGateway(ctx context.Context, labName, nodeName string) (string, error)
	// StartNodes resumes previously stopped (frozen) nodes.
	StartNodes(ctx context.Context, labName string, nodeNames []string) error
	// StopNodes freezes the named nodes in place. Freezing (not
	// container stop) keeps the veth wiring and injected network
	// config intact, so a later start restores the exact device.
	StopNodes(ctx context.Context, labName string, nodeNames []string) error
	// EnsureImage verifies a container image is available locally.
	EnsureImage(ctx context.Context, image string) error
	// ConnectInternet attaches a deployed node to the shared WAN
	// network and routes its non-fabric traffic to the real internet.
	ConnectInternet(ctx context.Context, labName, nodeName string) error
	// SetInterfaceState brings a node's interface administratively up
	// or down, for link-down and interface-down faults.
	SetInterfaceState(ctx context.Context, labName, nodeName, iface string, up bool) error
	// ApplyImpairment shapes egress traffic on a node's interface with
	// a netem qdisc built from imp.
	ApplyImpairment(ctx context.Context, labName, nodeName, iface string, imp Impairment) error
	// ClearImpairment removes a previously applied netem qdisc from a
	// node's interface; a no-op if none is present.
	ClearImpairment(ctx context.Context, labName, nodeName, iface string) error
}

// TerminalSession is a live interactive shell inside a lab node.
// Reads return terminal output, writes feed keystrokes; Close ends
// the underlying process.
type TerminalSession interface {
	io.ReadWriteCloser
	// Resize adjusts the pseudo-terminal window size.
	Resize(cols, rows uint16) error
}

// ExecSession is a long-lived non-interactive command inside a lab
// node. Reads return the command's raw stdout; Close closes its stdin
// — the in-container tool's stop signal — and reaps the process,
// reporting stderr if it exited abnormally.
type ExecSession interface {
	io.ReadCloser
}

// TopologyFileName is the artifact file Containerlab consumes.
const TopologyFileName = "topology.clab.yml"

// DefaultAgentAddr is the data plane agent's default gRPC listen
// address: localhost, matching the single-machine install where the
// controller runs on the same host. A dedicated data plane machine
// binds a routable address instead.
const DefaultAgentAddr = "127.0.0.1:50063"

// Impairment shapes egress traffic on one interface. It mirrors
// netem's own parameter set directly: a network interface carries at
// most one netem qdisc, so combining delay/jitter/loss/rate into one
// call (rather than one driver method per knob) is not a
// simplification of the API — it is the shape the underlying
// primitive already has. Zero fields are omitted from the qdisc.
type Impairment struct {
	DelayMs     int
	JitterMs    int
	LossPercent float64
	RateKbit    int
}

// NoopDriver writes artifacts but does not start containers. It keeps
// the full plan/apply/generation workflow usable without Containerlab.
type NoopDriver struct{}

func (NoopDriver) Name() string                                  { return "noop" }
func (NoopDriver) Deploy(ctx context.Context, dir string) error  { return nil }
func (NoopDriver) Destroy(ctx context.Context, dir string) error { return nil }

func (NoopDriver) Exec(ctx context.Context, labName, nodeName string, cmd []string) ([]byte, error) {
	return nil, ErrNotSupported
}

func (NoopDriver) OpenTerminal(ctx context.Context, labName, nodeName string, cmd []string) (TerminalSession, error) {
	return nil, ErrNotSupported
}

func (NoopDriver) ExecStream(ctx context.Context, labName, nodeName string, cmd []string) (ExecSession, error) {
	return nil, ErrNotSupported
}

func (NoopDriver) NodeStates(ctx context.Context, labName string, nodeNames []string) (map[string]string, error) {
	return nil, ErrNotSupported
}

func (NoopDriver) NodeAddress(ctx context.Context, labName, nodeName string) (string, error) {
	return "", ErrNotSupported
}

func (NoopDriver) NodeGateway(ctx context.Context, labName, nodeName string) (string, error) {
	return "", ErrNotSupported
}

func (NoopDriver) StartNodes(ctx context.Context, labName string, nodeNames []string) error {
	return ErrNotSupported
}

func (NoopDriver) StopNodes(ctx context.Context, labName string, nodeNames []string) error {
	return ErrNotSupported
}

// EnsureImage succeeds unconditionally: the noop driver never runs
// containers, so no image is actually needed.
func (NoopDriver) EnsureImage(ctx context.Context, image string) error { return nil }

func (NoopDriver) ConnectInternet(ctx context.Context, labName, nodeName string) error {
	return ErrNotSupported
}

func (NoopDriver) SetInterfaceState(ctx context.Context, labName, nodeName, iface string, up bool) error {
	return ErrNotSupported
}

func (NoopDriver) ApplyImpairment(ctx context.Context, labName, nodeName, iface string, imp Impairment) error {
	return ErrNotSupported
}

func (NoopDriver) ClearImpairment(ctx context.Context, labName, nodeName, iface string) error {
	return ErrNotSupported
}
