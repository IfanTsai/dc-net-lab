// Package agent implements dcnetlab-node-agent: a small process
// supervisor that runs inside every lab server container. It installs
// package archives from the controller's repository into a local
// package store and manages Programs (install, start/stop, restart
// policy, logs) running out of them, all under one state directory.
// It deliberately has no arbitrary-command surface; the controller
// talks to it via the gRPC service in server.go.
package nodeagent

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"sort"
	"sync"
	"syscall"
	"time"

	"github.com/ifantsai/dcnetlab/internal/nodeagentapi"
)

const (
	maxLogSize     = 5 << 20 // rotate program.log beyond this
	stopTimeout    = 5 * time.Second
	maxBackoff     = 15 * time.Second
	initialBackoff = time.Second
)

var nameRE = regexp.MustCompile(`^[a-z0-9][a-z0-9-]{0,62}$`)

// Spec is the agent-side program definition: an entrypoint of an
// installed package version plus its arguments. Type and AutoStart
// follow systemd service semantics: a simple program is a daemon, a
// oneshot runs to completion; an auto-start (enabled) program starts
// on every agent boot.
type Spec struct {
	Name           string   `json:"name"`
	PackageName    string   `json:"packageName"`
	PackageVersion string   `json:"packageVersion"`
	Entrypoint     string   `json:"entrypoint"`
	Args           []string `json:"args"`
	RestartPolicy  string   `json:"restartPolicy"`
	Type           string   `json:"type"`
	AutoStart      bool     `json:"autoStart"`
	// LivenessCheck, when set, restarts the program (per RestartPolicy)
	// once it fails FailureThreshold consecutive probes.
	LivenessCheck *HealthCheck `json:"livenessCheck,omitempty"`
	// ReadinessCheck, when set, gates Ready; unlike the liveness check
	// it never restarts the program.
	ReadinessCheck *HealthCheck `json:"readinessCheck,omitempty"`
	// StartupOrder sequences boot: programs start in ascending order
	// (ties by name). Default 0.
	StartupOrder int `json:"startupOrder,omitempty"`
}

// Info is a point-in-time snapshot of one program.
type Info struct {
	Spec      Spec
	State     string
	PID       int
	Restarts  int
	LastError string
	Health    string
	Ready     bool
	StartedAt time.Time
}

// meta is the persisted per-program state, enough to restore the
// desired situation after an agent restart.
type meta struct {
	Spec    Spec   `json:"spec"`
	Desired string `json:"desired"` // StateRunning or StateStopped
	PID     int    `json:"pid,omitempty"`
}

// program is the live supervision state of one installed program.
type program struct {
	spec      Spec
	state     string
	pid       int
	restarts  int
	lastErr   string
	health    string // nodeagentapi.Health*, empty until a liveness probe runs
	ready     bool   // readiness verdict; true when running without a readiness check
	startedAt time.Time
	desired   string

	cancel context.CancelFunc // non-nil while the supervisor runs
	done   chan struct{}
}

// Manager supervises the installed programs and owns the package
// store. Programs live in <dir>/programs/<name>, unpacked packages in
// <dir>/packages/<name>/<version>.
type Manager struct {
	dir string // state root
	log *slog.Logger

	mu       sync.Mutex
	programs map[string]*program

	pkgMu sync.Mutex // serialises package store mutations
}

// NewManager loads the persisted program definitions from dir and
// restores their desired state: stale processes from a previous
// agent life are killed, programs desired running are started again.
func NewManager(dir string, log *slog.Logger) (*Manager, error) {
	m := &Manager{dir: dir, log: log, programs: make(map[string]*program)}

	for _, sub := range []string{"programs", "packages"} {
		if err := os.MkdirAll(filepath.Join(dir, sub), 0o755); err != nil {
			return nil, fmt.Errorf("create state dir: %w", err)
		}
	}

	entries, err := os.ReadDir(filepath.Join(dir, "programs"))
	if err != nil {
		return nil, fmt.Errorf("read state dir: %w", err)
	}

	// Load every definition first, killing any orphan process from the
	// previous agent life, then start what boot semantics demand in
	// StartupOrder — so a program can rely on lower-order ones having
	// been launched before it.
	var toStart []*program
	for _, e := range entries {
		if !e.IsDir() {
			continue
		}

		mt, err := readMeta(m.metaPath(e.Name()))
		if err != nil {
			log.Warn("skip broken program state", "name", e.Name(), "error", err)

			continue
		}

		// A PID from a previous agent life is either gone (fresh
		// container) or an orphan that must not keep running next to
		// the new supervised instance.
		if mt.PID > 0 {
			_ = syscall.Kill(-mt.PID, syscall.SIGKILL)
		}

		p := &program{spec: mt.Spec, state: nodeagentapi.StateConfigured, desired: mt.Desired}
		m.programs[mt.Spec.Name] = p

		// Boot semantics: what was running comes back, and enabled
		// (auto-start) programs start regardless — systemd starts
		// enabled units at every boot, oneshots included.
		if mt.Desired == nodeagentapi.StateRunning || mt.Spec.AutoStart {
			toStart = append(toStart, p)
		}
	}

	sortByStartupOrder(toStart)
	for _, p := range toStart {
		p.desired = nodeagentapi.StateRunning
		if err := m.startLocked(p); err != nil {
			log.Warn("restore program", "name", p.spec.Name, "error", err)
		}

		_ = m.persistLocked(p)
	}

	return m, nil
}

// sortByStartupOrder orders programs by ascending StartupOrder, ties
// broken by name for a deterministic boot sequence.
func sortByStartupOrder(programs []*program) {
	sort.Slice(programs, func(i, j int) bool {
		if programs[i].spec.StartupOrder != programs[j].spec.StartupOrder {
			return programs[i].spec.StartupOrder < programs[j].spec.StartupOrder
		}

		return programs[i].spec.Name < programs[j].spec.Name
	})
}

// Install registers or replaces a program definition; a running
// program must be stopped first and the referenced package version
// must already be installed.
func (m *Manager) Install(spec Spec) (Info, error) {
	if !nameRE.MatchString(spec.Name) {
		return Info{}, fmt.Errorf("invalid program name %q", spec.Name)
	}

	switch spec.RestartPolicy {
	case nodeagentapi.RestartNever, nodeagentapi.RestartOnFailure, nodeagentapi.RestartAlways:
	case "":
		spec.RestartPolicy = nodeagentapi.RestartNever
	default:
		return Info{}, fmt.Errorf("invalid restart policy %q", spec.RestartPolicy)
	}

	switch spec.Type {
	case nodeagentapi.TypeSimple, nodeagentapi.TypeOneshot:
	case "":
		spec.Type = nodeagentapi.TypeSimple
	default:
		return Info{}, fmt.Errorf("invalid program type %q", spec.Type)
	}

	if spec.Type == nodeagentapi.TypeOneshot && spec.RestartPolicy == nodeagentapi.RestartAlways {
		return Info{}, fmt.Errorf("oneshot programs cannot use the Always restart policy")
	}

	var err error
	if spec.LivenessCheck, err = normaliseCheck(spec.LivenessCheck); err != nil {
		return Info{}, fmt.Errorf("liveness check: %w", err)
	}

	if spec.ReadinessCheck, err = normaliseCheck(spec.ReadinessCheck); err != nil {
		return Info{}, fmt.Errorf("readiness check: %w", err)
	}

	if _, err := m.entrypointPath(spec); err != nil {
		return Info{}, err
	}

	m.mu.Lock()
	defer m.mu.Unlock()

	if p, ok := m.programs[spec.Name]; ok && p.state == nodeagentapi.StateRunning {
		return Info{}, fmt.Errorf("program %q is running; stop it before reinstalling", spec.Name)
	}

	if err := os.MkdirAll(m.programDir(spec.Name), 0o755); err != nil {
		return Info{}, fmt.Errorf("create program dir: %w", err)
	}

	p := &program{spec: spec, state: nodeagentapi.StateConfigured, desired: nodeagentapi.StateStopped}
	m.programs[spec.Name] = p
	if err := m.persistLocked(p); err != nil {
		return Info{}, err
	}

	return p.info(), nil
}

// Start launches a program; starting a running program is a no-op.
func (m *Manager) Start(name string) (Info, error) {
	m.mu.Lock()
	defer m.mu.Unlock()

	p, ok := m.programs[name]
	if !ok {
		return Info{}, fmt.Errorf("program %q not installed", name)
	}

	if p.state == nodeagentapi.StateRunning {
		return p.info(), nil
	}

	p.desired = nodeagentapi.StateRunning
	if err := m.startLocked(p); err != nil {
		return Info{}, err
	}

	if err := m.persistLocked(p); err != nil {
		return Info{}, err
	}

	return p.info(), nil
}

// Stop terminates a program and disables its restart policy until
// the next Start. Stopping a stopped program is a no-op.
func (m *Manager) Stop(name string) (Info, error) {
	m.mu.Lock()
	p, ok := m.programs[name]
	if !ok {
		m.mu.Unlock()

		return Info{}, fmt.Errorf("program %q not installed", name)
	}

	p.desired = nodeagentapi.StateStopped
	if err := m.persistLocked(p); err != nil {
		m.mu.Unlock()

		return Info{}, err
	}

	m.mu.Unlock()

	if err := m.terminate(name); err != nil {
		return Info{}, err
	}

	m.mu.Lock()
	defer m.mu.Unlock()

	return p.info(), nil
}

// terminate cancels a program's supervision and waits for the
// process to be gone, leaving the persisted desired state untouched.
func (m *Manager) terminate(name string) error {
	m.mu.Lock()
	p, ok := m.programs[name]
	if !ok {
		m.mu.Unlock()

		return fmt.Errorf("program %q not installed", name)
	}

	cancel, done := p.cancel, p.done
	m.mu.Unlock()

	if cancel == nil {
		return nil
	}

	cancel()
	select {
	case <-done:
	case <-time.After(stopTimeout + 2*time.Second):
		return fmt.Errorf("program %q did not stop in time", name)
	}

	return nil
}

// Remove stops a program and deletes its definition and logs.
func (m *Manager) Remove(name string) error {
	if _, err := m.Stop(name); err != nil {
		return err
	}

	m.mu.Lock()
	defer m.mu.Unlock()

	delete(m.programs, name)

	if err := os.RemoveAll(m.programDir(name)); err != nil {
		return fmt.Errorf("remove program dir: %w", err)
	}

	return nil
}

// List snapshots every installed program, sorted by name.
func (m *Manager) List() []Info {
	m.mu.Lock()
	defer m.mu.Unlock()

	infos := make([]Info, 0, len(m.programs))
	for _, p := range m.programs {
		infos = append(infos, p.info())
	}

	sort.Slice(infos, func(i, j int) bool { return infos[i].Spec.Name < infos[j].Spec.Name })

	return infos
}

// TailLogs returns the last n lines of a program's combined log.
func (m *Manager) TailLogs(name string, n int) (string, error) {
	m.mu.Lock()
	_, ok := m.programs[name]
	m.mu.Unlock()

	if !ok {
		return "", fmt.Errorf("program %q not installed", name)
	}

	return tailFile(m.logPath(name), n)
}

// Shutdown kills every running process on agent termination without
// touching the persisted desired state, so the next agent life
// restores what was running — a supervisor restart must not read as
// "the user stopped these programs" (systemd: restarting systemd
// does not disable services).
func (m *Manager) Shutdown() {
	for _, info := range m.List() {
		if info.State == nodeagentapi.StateRunning {
			if err := m.terminate(info.Spec.Name); err != nil {
				m.log.Warn("shutdown stop", "program", info.Spec.Name, "error", err)
			}
		}
	}
}

// entrypointPath resolves a spec's executable inside the package
// store and verifies it exists.
func (m *Manager) entrypointPath(spec Spec) (string, error) {
	if spec.Entrypoint == "" || !filepath.IsLocal(spec.Entrypoint) {
		return "", fmt.Errorf("invalid entrypoint %q", spec.Entrypoint)
	}

	path := filepath.Join(m.packageDir(spec.PackageName, spec.PackageVersion), spec.Entrypoint)
	if st, err := os.Stat(path); err != nil || !st.Mode().IsRegular() {
		return "", fmt.Errorf("package %s@%s not installed (missing %s)",
			spec.PackageName, spec.PackageVersion, spec.Entrypoint)
	}

	return path, nil
}

// startLocked spawns the first process synchronously (so the caller
// sees the PID or the spawn error) and hands it to the supervision
// loop; m.mu must be held.
func (m *Manager) startLocked(p *program) error {
	cmd, logFile, err := m.spawn(p.spec)
	if err != nil {
		p.state = nodeagentapi.StateFailed
		p.lastErr = err.Error()

		return err
	}

	ctx, cancel := context.WithCancel(context.Background())
	p.cancel = cancel
	p.done = make(chan struct{})
	p.state = nodeagentapi.StateRunning
	p.restarts = 0
	p.lastErr = ""
	p.health = nodeagentapi.HealthUnknown
	// A program with no readiness check is ready as soon as it runs
	// (like a systemd Type=simple unit); one with a check starts not
	// ready until its first successful probe.
	p.ready = p.spec.ReadinessCheck == nil
	p.pid = cmd.Process.Pid
	p.startedAt = time.Now().UTC()

	m.log.Info("program started", "name", p.spec.Name, "pid", p.pid)
	go m.supervise(ctx, p, cmd, logFile)
	if p.spec.LivenessCheck != nil {
		go m.monitorLiveness(ctx, p, *p.spec.LivenessCheck)
	}

	if p.spec.ReadinessCheck != nil {
		go m.monitorReadiness(ctx, p, *p.spec.ReadinessCheck)
	}

	return nil
}

// monitorReadiness probes a running program on its check interval and
// tracks whether it is serving. Unlike the liveness monitor it never
// restarts: readiness only reflects the latest verdict, and falls back
// to not-ready whenever the program is not running.
func (m *Manager) monitorReadiness(ctx context.Context, p *program, check HealthCheck) {
	ticker := time.NewTicker(check.Interval)
	defer ticker.Stop()

	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
		}

		m.mu.Lock()
		pid, running := p.pid, p.state == nodeagentapi.StateRunning
		m.mu.Unlock()

		if !running || pid == 0 {
			m.setReady(p, false)

			continue
		}

		m.setReady(p, check.probe(ctx, pid))
	}
}

// setReady records the latest readiness verdict, but only while the
// program is running: a probe racing a stop must not resurrect Ready.
func (m *Manager) setReady(p *program, ready bool) {
	m.mu.Lock()
	defer m.mu.Unlock()

	if p.state == nodeagentapi.StateRunning {
		p.ready = ready
	}
}

// monitorLiveness probes a running program on its check interval and,
// after FailureThreshold consecutive failures, kills the current
// process so the supervision loop restarts it per the restart policy.
// It lives for one supervision context (across restarts): the failure
// count resets whenever a new incarnation appears, and it never probes
// while the program is not running.
func (m *Manager) monitorLiveness(ctx context.Context, p *program, check HealthCheck) {
	ticker := time.NewTicker(check.Interval)
	defer ticker.Stop()

	failures := 0
	monitored := 0
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
		}

		m.mu.Lock()
		pid, running := p.pid, p.state == nodeagentapi.StateRunning
		m.mu.Unlock()

		if !running || pid == 0 {
			failures, monitored = 0, 0

			continue
		}

		if pid != monitored {
			monitored, failures = pid, 0
		}

		if check.probe(ctx, pid) {
			failures = 0
			m.setHealth(p, nodeagentapi.HealthHealthy)

			continue
		}

		failures++
		if failures < check.FailureThreshold {
			continue
		}

		m.setHealth(p, nodeagentapi.HealthUnhealthy)
		m.log.Warn("liveness check failed; restarting program",
			"name", p.spec.Name, "pid", pid, "failures", failures)
		_ = syscall.Kill(-pid, syscall.SIGTERM)
		failures, monitored = 0, 0
	}
}

// setHealth records the latest liveness verdict, but only while the
// program is still running: a probe racing a stop must not overwrite
// the terminal health reset.
func (m *Manager) setHealth(p *program, health string) {
	m.mu.Lock()
	defer m.mu.Unlock()

	if p.state == nodeagentapi.StateRunning {
		p.health = health
	}
}

// spawn launches one process instance with its output appended to
// the program log.
func (m *Manager) spawn(spec Spec) (*exec.Cmd, *os.File, error) {
	binary, err := m.entrypointPath(spec)
	if err != nil {
		return nil, nil, err
	}

	m.rotateLog(spec.Name)

	logFile, err := os.OpenFile(m.logPath(spec.Name), os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o644)
	if err != nil {
		return nil, nil, fmt.Errorf("open log: %w", err)
	}

	cmd := exec.Command(binary, spec.Args...)
	cmd.Stdout = logFile
	cmd.Stderr = logFile
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}

	if err := cmd.Start(); err != nil {
		_ = logFile.Close()

		return nil, nil, fmt.Errorf("start: %w", err)
	}

	return cmd, logFile, nil
}

// supervise waits on the running process and restarts it according
// to the restart policy with exponential backoff; cancellation kills
// the process group and ends the loop.
func (m *Manager) supervise(ctx context.Context, p *program, cmd *exec.Cmd, logFile *os.File) {
	defer close(p.done)

	backoff := initialBackoff
	for {
		exitErr := m.waitProcess(ctx, cmd)
		_ = logFile.Close()

		m.mu.Lock()
		p.pid = 0
		p.health = nodeagentapi.HealthUnknown // no live process to probe
		p.ready = false
		restart := false
		switch {
		case ctx.Err() != nil || p.desired != nodeagentapi.StateRunning:
			p.state = nodeagentapi.StateStopped
		case p.spec.RestartPolicy == nodeagentapi.RestartAlways:
			restart = true
		case p.spec.RestartPolicy == nodeagentapi.RestartOnFailure && exitErr != nil:
			restart = true
		case exitErr != nil:
			p.state = nodeagentapi.StateFailed
			p.lastErr = exitErr.Error()
		case p.spec.Type == nodeagentapi.TypeOneshot:
			// Ran to completion (systemd: a oneshot unit goes inactive).
			// Desired flips to stopped so only auto-start re-runs it on
			// the next boot.
			p.state = nodeagentapi.StateExited
			p.desired = nodeagentapi.StateStopped
		default:
			p.state = nodeagentapi.StateStopped
		}

		if restart && exitErr != nil {
			p.lastErr = exitErr.Error()
		}

		_ = m.persistLocked(p)
		m.mu.Unlock()

		if !restart {
			return
		}

		m.log.Info("restarting program", "name", p.spec.Name, "backoff", backoff.String())
		select {
		case <-ctx.Done():
			m.mu.Lock()
			p.state = nodeagentapi.StateStopped
			m.mu.Unlock()

			return
		case <-time.After(backoff):
		}

		backoff = min(backoff*2, maxBackoff)

		next, nextLog, err := m.spawn(p.spec)

		m.mu.Lock()
		if err != nil {
			p.state = nodeagentapi.StateFailed
			p.lastErr = err.Error()
			m.mu.Unlock()

			return
		}

		p.restarts++
		p.pid = next.Process.Pid
		p.startedAt = time.Now().UTC()
		p.state = nodeagentapi.StateRunning
		// A fresh incarnation is ready at once without a readiness check,
		// or not ready until its probe passes again.
		p.ready = p.spec.ReadinessCheck == nil
		_ = m.persistLocked(p)
		m.mu.Unlock()

		cmd, logFile = next, nextLog
	}
}

// waitProcess waits for the process to exit and returns its exit
// error. Cancellation kills the whole process group: SIGTERM first,
// SIGKILL after stopTimeout.
func (m *Manager) waitProcess(ctx context.Context, cmd *exec.Cmd) error {
	waitCh := make(chan error, 1)
	go func() { waitCh <- cmd.Wait() }()

	select {
	case err := <-waitCh:
		return err
	case <-ctx.Done():
		_ = syscall.Kill(-cmd.Process.Pid, syscall.SIGTERM)
		select {
		case err := <-waitCh:
			return err
		case <-time.After(stopTimeout):
			_ = syscall.Kill(-cmd.Process.Pid, syscall.SIGKILL)

			return <-waitCh
		}
	}
}

// rotateLog keeps the program log bounded: beyond maxLogSize the
// current file replaces the single .1 predecessor.
func (m *Manager) rotateLog(name string) {
	path := m.logPath(name)

	st, err := os.Stat(path)
	if err != nil || st.Size() < maxLogSize {
		return
	}

	_ = os.Rename(path, path+".1")
}

func (m *Manager) programDir(name string) string {
	return filepath.Join(m.dir, "programs", name)
}

func (m *Manager) metaPath(name string) string {
	return filepath.Join(m.programDir(name), "meta.json")
}

func (m *Manager) logPath(name string) string {
	return filepath.Join(m.programDir(name), "program.log")
}

// persistLocked writes the program meta; m.mu must be held.
func (m *Manager) persistLocked(p *program) error {
	doc, err := json.Marshal(meta{Spec: p.spec, Desired: p.desired, PID: p.pid})
	if err != nil {
		return fmt.Errorf("marshal meta: %w", err)
	}

	if err := os.WriteFile(m.metaPath(p.spec.Name), doc, 0o644); err != nil {
		return fmt.Errorf("write meta: %w", err)
	}

	return nil
}

func readMeta(path string) (meta, error) {
	doc, err := os.ReadFile(path)
	if err != nil {
		return meta{}, err
	}

	var mt meta
	if err := json.Unmarshal(doc, &mt); err != nil {
		return meta{}, err
	}

	if !nameRE.MatchString(mt.Spec.Name) {
		return meta{}, fmt.Errorf("invalid program name %q", mt.Spec.Name)
	}

	return mt, nil
}

// info snapshots the program; the manager lock must be held.
func (p *program) info() Info {
	return Info{
		Spec:      p.spec,
		State:     p.state,
		PID:       p.pid,
		Restarts:  p.restarts,
		LastError: p.lastErr,
		Health:    p.health,
		Ready:     p.ready,
		StartedAt: p.startedAt,
	}
}

// tailFile returns the last n lines of path; a missing file is an
// empty log, not an error.
func tailFile(path string, n int) (string, error) {
	data, err := os.ReadFile(path)
	if os.IsNotExist(err) {
		return "", nil
	}

	if err != nil {
		return "", fmt.Errorf("read log: %w", err)
	}

	lines := 0
	i := len(data)
	for i > 0 {
		j := lastLineStart(data, i)
		lines++
		if lines >= n {
			i = j

			break
		}

		i = j
	}

	return string(data[i:]), nil
}

// lastLineStart returns the start index of the line ending at end.
func lastLineStart(data []byte, end int) int {
	if end > 0 && data[end-1] == '\n' {
		end--
	}

	for end > 0 && data[end-1] != '\n' {
		end--
	}

	return end
}
