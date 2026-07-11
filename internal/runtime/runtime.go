// Package runtime abstracts how a compiled lab artifact is realised.
// The first backend shells out to Containerlab; a noop backend keeps
// the control plane usable on machines without it.
package runtime

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"

	"github.com/creack/pty"

	"github.com/ifantsai/dcnetlab/internal/compiler"
)

// ErrNotSupported is returned by drivers that cannot perform an
// operation (e.g. exec on the noop driver). Callers treat it as
// "skip", not as a failure.
var ErrNotSupported = errors.New("operation not supported by this runtime driver")

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
	// NodeStates reports the live container state ("running",
	// "paused", "exited", ... or "missing") of each named node.
	NodeStates(ctx context.Context, labName string, nodeNames []string) (map[string]string, error)
	// StartNodes resumes previously stopped (frozen) nodes.
	StartNodes(ctx context.Context, labName string, nodeNames []string) error
	// StopNodes freezes the named nodes in place. Freezing (not
	// container stop) keeps the veth wiring and injected network
	// config intact, so a later start restores the exact device.
	StopNodes(ctx context.Context, labName string, nodeNames []string) error
}

// TerminalSession is a live interactive shell inside a lab node.
// Reads return terminal output, writes feed keystrokes; Close ends
// the underlying process.
type TerminalSession interface {
	io.ReadWriteCloser
	// Resize adjusts the pseudo-terminal window size.
	Resize(cols, rows uint16) error
}

// TopologyFileName is the artifact file Containerlab consumes.
const TopologyFileName = "topology.clab.yml"

// WriteArtifact materialises an artifact into dir so a driver (or a
// human) can deploy it. Paths inside the artifact are validated to
// stay under dir.
func WriteArtifact(art *compiler.Artifact, dir string) error {
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return err
	}

	files := map[string][]byte{TopologyFileName: art.ClabTopology}
	for rel, content := range art.Files {
		files[rel] = content
	}

	for rel, content := range files {
		path := filepath.Join(dir, rel)
		if !filepath.IsLocal(rel) {
			return fmt.Errorf("artifact path %q escapes generation directory", rel)
		}

		if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
			return err
		}

		if err := os.WriteFile(path, content, 0o644); err != nil {
			return err
		}
	}

	return nil
}

// ContainerlabDriver drives the containerlab CLI.
type ContainerlabDriver struct {
	// Binary is the containerlab executable, default "containerlab".
	Binary string
}

func (d *ContainerlabDriver) Name() string { return "containerlab" }

func (d *ContainerlabDriver) bin() string {
	if d.Binary != "" {
		return d.Binary
	}

	return "containerlab"
}

func (d *ContainerlabDriver) run(ctx context.Context, dir string, args ...string) error {
	cmd := exec.CommandContext(ctx, d.bin(), args...)
	cmd.Dir = dir

	out, err := cmd.CombinedOutput()
	if err != nil {
		return fmt.Errorf("containerlab %v: %w\n%s", args, err, out)
	}

	return nil
}

func (d *ContainerlabDriver) Deploy(ctx context.Context, dir string) error {
	return d.run(ctx, dir, "deploy", "-t", TopologyFileName, "--reconfigure")
}

func (d *ContainerlabDriver) Destroy(ctx context.Context, dir string) error {
	return d.run(ctx, dir, "destroy", "-t", TopologyFileName, "--cleanup")
}

// Available reports whether the containerlab binary can be found.
func (d *ContainerlabDriver) Available() bool {
	_, err := exec.LookPath(d.bin())

	return err == nil
}

// Exec runs a command in a lab container via docker. Containerlab
// names containers clab-<labName>-<nodeName>.
func (d *ContainerlabDriver) Exec(ctx context.Context, labName, nodeName string, cmdArgs []string) ([]byte, error) {
	container := fmt.Sprintf("clab-%s-%s", labName, nodeName)
	args := append([]string{"exec", container}, cmdArgs...)

	out, err := exec.CommandContext(ctx, "docker", args...).CombinedOutput()
	if err != nil {
		return out, fmt.Errorf("docker exec %s %v: %w\n%s", container, cmdArgs, err, out)
	}

	return out, nil
}

// OpenTerminal attaches an interactive shell to a lab container: it
// runs `docker exec -it` under a local pseudo-terminal so line
// editing, signals and window resizing behave like a real login.
func (d *ContainerlabDriver) OpenTerminal(ctx context.Context, labName, nodeName string, cmdArgs []string) (TerminalSession, error) {
	container := fmt.Sprintf("clab-%s-%s", labName, nodeName)
	args := append([]string{"exec", "-it", container}, cmdArgs...)

	cmd := exec.CommandContext(ctx, "docker", args...)
	f, err := pty.Start(cmd)
	if err != nil {
		return nil, fmt.Errorf("open terminal in %s: %w", container, err)
	}

	return &ptySession{f: f, cmd: cmd}, nil
}

// StartNodes thaws frozen containers with docker unpause. Only
// containers that are actually paused are touched: docker refuses to
// unpause a running container, and the recorded node phase can drift
// from container state (e.g. after a partially failed batch), so the
// filter goes by live docker status to stay idempotent.
func (d *ContainerlabDriver) StartNodes(ctx context.Context, labName string, nodeNames []string) error {
	paused, err := d.containersWithStatus(ctx, "paused")
	if err != nil {
		return err
	}

	return d.docker(ctx, labName, filterByStatus(labName, nodeNames, paused), "unpause")
}

// StopNodes freezes containers with docker pause: a stopped container
// would lose its veth links and exec-injected config (bridge, bond,
// VLANs), while a paused one keeps them and just goes silent — the
// right semantics for simulating a device power-off. Neighbours see
// BGP hold timers expire and VRRP fail over, exactly like a real
// outage. Mirroring StartNodes, only currently running containers
// are paused.
func (d *ContainerlabDriver) StopNodes(ctx context.Context, labName string, nodeNames []string) error {
	running, err := d.containersWithStatus(ctx, "running")
	if err != nil {
		return err
	}

	return d.docker(ctx, labName, filterByStatus(labName, nodeNames, running), "pause")
}

// NodeStates returns each node's docker container state in one
// docker ps sweep; nodes whose container does not exist are reported
// as "missing".
func (d *ContainerlabDriver) NodeStates(ctx context.Context, labName string, nodeNames []string) (map[string]string, error) {
	out, err := exec.CommandContext(ctx, "docker",
		"ps", "-a", "--format", "{{.Names}}\t{{.State}}").Output()
	if err != nil {
		return nil, fmt.Errorf("docker ps: %w", err)
	}

	byContainer := make(map[string]string)
	for _, line := range strings.Split(string(out), "\n") {
		if name, state, ok := strings.Cut(strings.TrimSpace(line), "\t"); ok {
			byContainer[name] = state
		}
	}

	states := make(map[string]string, len(nodeNames))
	for _, n := range nodeNames {
		state, ok := byContainer[fmt.Sprintf("clab-%s-%s", labName, n)]
		if !ok {
			state = "missing"
		}

		states[n] = state
	}

	return states, nil
}

// containersWithStatus returns the names of containers currently in
// the given docker status ("running", "paused", ...).
func (d *ContainerlabDriver) containersWithStatus(ctx context.Context, status string) (map[string]bool, error) {
	out, err := exec.CommandContext(ctx, "docker",
		"ps", "--filter", "status="+status, "--format", "{{.Names}}").Output()
	if err != nil {
		return nil, fmt.Errorf("docker ps: %w", err)
	}

	set := make(map[string]bool)
	for _, name := range strings.Fields(string(out)) {
		set[name] = true
	}

	return set, nil
}

// filterByStatus keeps the node names whose container is in the set.
func filterByStatus(labName string, nodeNames []string, set map[string]bool) []string {
	kept := make([]string, 0, len(nodeNames))
	for _, n := range nodeNames {
		if set[fmt.Sprintf("clab-%s-%s", labName, n)] {
			kept = append(kept, n)
		}
	}

	return kept
}

func (d *ContainerlabDriver) docker(ctx context.Context, labName string, nodeNames []string, args ...string) error {
	if len(nodeNames) == 0 {
		return nil
	}

	for _, n := range nodeNames {
		args = append(args, fmt.Sprintf("clab-%s-%s", labName, n))
	}

	out, err := exec.CommandContext(ctx, "docker", args...).CombinedOutput()
	if err != nil {
		return fmt.Errorf("docker %s: %w\n%s", args[0], err, out)
	}

	return nil
}

// ptySession wraps the pty master side of a running docker exec.
type ptySession struct {
	f   *os.File
	cmd *exec.Cmd
}

func (s *ptySession) Read(p []byte) (int, error)  { return s.f.Read(p) }
func (s *ptySession) Write(p []byte) (int, error) { return s.f.Write(p) }

func (s *ptySession) Resize(cols, rows uint16) error {
	return pty.Setsize(s.f, &pty.Winsize{Cols: cols, Rows: rows})
}

// Close ends the session: closing the pty hangs up the shell, then
// the process is killed and reaped so no zombie is left behind.
func (s *ptySession) Close() error {
	err := s.f.Close()
	if s.cmd.Process != nil {
		_ = s.cmd.Process.Kill()
	}

	_ = s.cmd.Wait()

	return err
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

func (NoopDriver) NodeStates(ctx context.Context, labName string, nodeNames []string) (map[string]string, error) {
	return nil, ErrNotSupported
}

func (NoopDriver) StartNodes(ctx context.Context, labName string, nodeNames []string) error {
	return ErrNotSupported
}

func (NoopDriver) StopNodes(ctx context.Context, labName string, nodeNames []string) error {
	return ErrNotSupported
}
