// Package clab implements the runtime driver over the containerlab
// and docker CLIs. It runs on the machine hosting the lab containers;
// the data plane agent exposes it to the controller over gRPC.
package clab

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"os"
	"os/exec"
	"strings"
	"time"

	"github.com/creack/pty"

	"github.com/ifantsai/dcnetlab/internal/runtime"
)

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
	return d.run(ctx, dir, "deploy", "-t", runtime.TopologyFileName, "--reconfigure")
}

func (d *ContainerlabDriver) Destroy(ctx context.Context, dir string) error {
	return d.run(ctx, dir, "destroy", "-t", runtime.TopologyFileName, "--cleanup")
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
		if daemonUnavailable(out) {
			return out, fmt.Errorf("%w: docker exec %s: %s", runtime.ErrUnavailable, container, strings.TrimSpace(string(out)))
		}

		return out, fmt.Errorf("docker exec %s %v: %w\n%s", container, cmdArgs, err, out)
	}

	return out, nil
}

// daemonUnavailable reports whether docker CLI output indicates the
// daemon itself is unreachable (not running, or the socket denying
// access) rather than a container-level failure.
func daemonUnavailable(out []byte) bool {
	s := strings.ToLower(string(out))

	return strings.Contains(s, "cannot connect to the docker daemon") ||
		strings.Contains(s, "is the docker daemon running") ||
		strings.Contains(s, "permission denied while trying to connect")
}

// OpenTerminal attaches an interactive shell to a lab container: it
// runs `docker exec -it` under a local pseudo-terminal so line
// editing, signals and window resizing behave like a real login.
func (d *ContainerlabDriver) OpenTerminal(ctx context.Context, labName, nodeName string, cmdArgs []string) (runtime.TerminalSession, error) {
	container := fmt.Sprintf("clab-%s-%s", labName, nodeName)
	args := append([]string{"exec", "-it", container}, cmdArgs...)

	cmd := exec.CommandContext(ctx, "docker", args...)
	f, err := pty.Start(cmd)
	if err != nil {
		return nil, fmt.Errorf("open terminal in %s: %w", container, err)
	}

	return &ptySession{f: f, cmd: cmd}, nil
}

// ExecStream starts `docker exec -i` with stdin piped and stdout
// streamed back. It backs long-lived in-container tools (capture):
// they treat stdin EOF as their stop signal, so whichever way the
// session ends — Close, a cancelled context or a dropped connection —
// the tool exits and no orphan is left in the container.
func (d *ContainerlabDriver) ExecStream(ctx context.Context, labName, nodeName string, cmdArgs []string) (runtime.ExecSession, error) {
	container := fmt.Sprintf("clab-%s-%s", labName, nodeName)
	args := append([]string{"exec", "-i", container}, cmdArgs...)

	cmd := exec.CommandContext(ctx, "docker", args...)
	stdin, err := cmd.StdinPipe()
	if err != nil {
		return nil, fmt.Errorf("exec stream in %s: %w", container, err)
	}

	stdout, err := cmd.StdoutPipe()
	if err != nil {
		return nil, fmt.Errorf("exec stream in %s: %w", container, err)
	}

	var stderr bytes.Buffer
	cmd.Stderr = &stderr

	if err := cmd.Start(); err != nil {
		return nil, fmt.Errorf("exec stream in %s: %w", container, err)
	}

	return &execStream{container: container, cmd: cmd, stdin: stdin, stdout: stdout, stderr: &stderr}, nil
}

// execStream wraps a running `docker exec -i`.
type execStream struct {
	container string
	cmd       *exec.Cmd
	stdin     io.WriteCloser
	stdout    io.ReadCloser
	stderr    *bytes.Buffer
}

func (s *execStream) Read(p []byte) (int, error) { return s.stdout.Read(p) }

// Close closes the command's stdin — the in-container tool exits on
// that EOF, which ends the exec — then reaps the process. If the tool
// ignores the EOF, the docker client is killed after a grace period;
// its dropped connection closes the container-side stdin as well, so
// the tool still terminates.
func (s *execStream) Close() error {
	_ = s.stdin.Close()

	done := make(chan error, 1)
	go func() { done <- s.cmd.Wait() }()

	var err error
	select {
	case err = <-done:
	case <-time.After(5 * time.Second):
		if s.cmd.Process != nil {
			_ = s.cmd.Process.Kill()
		}

		err = <-done
	}

	if err != nil {
		return fmt.Errorf("exec stream in %s: %w\n%s", s.container, err, s.stderr.Bytes())
	}

	return nil
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

// NodeAddress reads the container's management IP via docker inspect
// (containerlab attaches exactly one network, its management bridge).
func (d *ContainerlabDriver) NodeAddress(ctx context.Context, labName, nodeName string) (string, error) {
	return d.inspectNetwork(ctx, labName, nodeName, "IPAddress", "management address")
}

// NodeGateway reads the host-side gateway IP of the container's
// management network via docker inspect.
func (d *ContainerlabDriver) NodeGateway(ctx context.Context, labName, nodeName string) (string, error) {
	return d.inspectNetwork(ctx, labName, nodeName, "Gateway", "management gateway")
}

// inspectNetwork extracts one field of the container's management
// network. External routers carry a second, WAN attachment, which is
// skipped by name so the management address stays unambiguous.
func (d *ContainerlabDriver) inspectNetwork(ctx context.Context, labName, nodeName, field, what string) (string, error) {
	container := fmt.Sprintf("clab-%s-%s", labName, nodeName)

	out, err := exec.CommandContext(ctx, "docker", "inspect", "-f",
		fmt.Sprintf(`{{range $name, $net := .NetworkSettings.Networks}}{{if ne $name %q}}{{$net.%s}}{{end}}{{end}}`,
			WANNetworkName, field), container).Output()
	if err != nil {
		return "", fmt.Errorf("docker inspect %s: %w", container, err)
	}

	addr := strings.TrimSpace(string(out))
	if addr == "" {
		return "", fmt.Errorf("container %s has no %s", container, what)
	}

	return addr, nil
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
