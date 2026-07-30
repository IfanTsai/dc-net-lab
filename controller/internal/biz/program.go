package biz

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net"
	"regexp"
	"sort"
	"strconv"
	"time"

	"github.com/ifantsai/dcnetlab/controller/internal/conf"
	"github.com/ifantsai/dcnetlab/controller/internal/metrics"
	"github.com/ifantsai/dcnetlab/internal/model"
	"github.com/ifantsai/dcnetlab/internal/nodeagentapi"
	"github.com/ifantsai/dcnetlab/internal/runtime"
)

// programNameRE mirrors the agent's program name constraint.
var programNameRE = regexp.MustCompile(`^[a-z0-9][a-z0-9-]{0,62}$`)

// validateHealthCheck rejects a malformed liveness/readiness check
// before it reaches the agent; the agent applies the timing defaults.
// A nil check means the program is unmonitored. kind labels the check
// in error messages.
func validateHealthCheck(kind string, hc *model.HealthCheck) error {
	if hc == nil {
		return nil
	}

	switch hc.Type {
	case nodeagentapi.CheckProcess:
	case nodeagentapi.CheckTCP, nodeagentapi.CheckHTTP:
		if hc.Port <= 0 || hc.Port > 65535 {
			return fmt.Errorf("%s check port %d out of range", kind, hc.Port)
		}
	default:
		return fmt.Errorf("invalid %s check type %q", kind, hc.Type)
	}

	return nil
}

// agentRestoreTimeout bounds how long RestorePrograms waits for the
// freshly exec-started agents to accept connections after a deploy.
const agentRestoreTimeout = 30 * time.Second

// ProgramRepo abstracts the persistence the program usecase needs.
type ProgramRepo interface {
	CreateProgram(p *model.Program) error
	UpdateProgram(p *model.Program) error
	DeleteProgram(id string) error
	GetProgram(id string) (*model.Program, error)
	ListPrograms(labID string) ([]*model.Program, error)
	GetLab(id string) (*model.Lab, error)
	ListNodes(labID string) ([]*model.Node, error)
}

// AgentPackage tells an agent which package version to install and
// where to fetch it from the controller's repository.
type AgentPackage struct {
	Name       string
	Version    string
	SHA256     string
	URL        string
	Entrypoint string
	Links      []string
}

// ProgramAgent is the node-agent client slice the usecase needs;
// the data layer implements it over gRPC. addr is the agent address
// (management IP, agent port appended by the implementation).
type ProgramAgent interface {
	InstallPackage(ctx context.Context, addr string, pkg AgentPackage) error
	Install(ctx context.Context, addr string, p *model.Program) error
	Start(ctx context.Context, addr, name string) (model.ProgramStatus, error)
	Stop(ctx context.Context, addr, name string) (model.ProgramStatus, error)
	Remove(ctx context.Context, addr, name string) error
	List(ctx context.Context, addr string) (map[string]model.ProgramStatus, error)
	ListPrograms(ctx context.Context, addr string) ([]model.NodeProgram, error)
	ListPackages(ctx context.Context, addr string) ([]model.InstalledPackage, error)
	TailLogs(ctx context.Context, addr, name string, lines int) (string, error)
	// Metrics scrapes a server's Prometheus endpoint: cumulative
	// counters and instantaneous gauges, no rates (callers diff
	// against their own baselines).
	Metrics(ctx context.Context, addr string) (*model.NodeMetrics, error)
}

// MetricsHistory is the collected resource-usage time series the
// usecase queries; internal/metrics implements it. Last serves as the
// diff baseline for the realtime view.
type MetricsHistory interface {
	Query(labID, server string, start, end time.Time) []model.MetricsPoint
	Last(labID, server string) (model.MetricsPoint, bool)
}

// historyDefaultRange is the window served when the request does not
// bound the series.
const historyDefaultRange = 30 * time.Minute

// ProgramUsecase manages Programs: supervised processes on lab
// servers, each running the entrypoint of a Package version.
// Desired state lives in the controller store; the node-agents
// only hold the running incarnation, so a redeploy is recovered by
// pushing the store (packages first, then programs) back onto the
// fresh agents.
type ProgramUsecase struct {
	repo     ProgramRepo
	agent    ProgramAgent
	driver   runtime.Driver
	packages *PackageUsecase
	history  MetricsHistory
	repoPort int
	log      *slog.Logger
}

// NewProgramUsecase wires the program usecase. The repository port
// comes from the configured listen address; lab servers reach it via
// their management gateway.
func NewProgramUsecase(repo ProgramRepo, agent ProgramAgent, driver runtime.Driver,
	packages *PackageUsecase, history MetricsHistory, c *conf.Server, log *slog.Logger,
) *ProgramUsecase {
	return &ProgramUsecase{
		repo: repo, agent: agent, driver: driver, packages: packages, history: history,
		repoPort: repoPortOf(c.RepoAddr, log), log: log,
	}
}

// repoPortOf extracts the port of the package repository listen
// address; 0 disables agent-side package installation.
func repoPortOf(addr string, log *slog.Logger) int {
	if addr == "" {
		return 0
	}

	_, portStr, err := net.SplitHostPort(addr)
	if err != nil {
		log.Warn("invalid repo listen address; package delivery disabled", "addr", addr, "error", err)

		return 0
	}

	port, err := strconv.Atoi(portStr)
	if err != nil {
		log.Warn("invalid repo listen port; package delivery disabled", "addr", addr, "error", err)

		return 0
	}

	return port
}

// CreateProgram validates one program definition and creates it on
// every requested server (one Program resource per server, like a
// daemonset over the selection). Programs start stopped unless start
// is set, which launches each right away (a oneshot runs once). Per-
// server problems (duplicate name, non-server target) are reported in
// the failures slice without aborting the rest.
func (uc *ProgramUsecase) CreateProgram(labID, name string, spec model.ProgramSpec, serverIDs []string, start bool) ([]*model.Program, []ServerInstall, error) {
	if !programNameRE.MatchString(name) {
		return nil, nil, fmt.Errorf("invalid program name %q (lowercase letters, digits, dashes)", name)
	}

	if len(serverIDs) == 0 {
		return nil, nil, fmt.Errorf("at least one server is required")
	}

	pkg, err := uc.packages.GetPackage(spec.PackageName, spec.PackageVersion)
	if err != nil {
		return nil, nil, fmt.Errorf("package %s@%s: %w", spec.PackageName, spec.PackageVersion, err)
	}

	switch spec.RestartPolicy {
	case nodeagentapi.RestartNever, nodeagentapi.RestartOnFailure, nodeagentapi.RestartAlways:
	case "":
		spec.RestartPolicy = nodeagentapi.RestartNever
	default:
		return nil, nil, fmt.Errorf("invalid restart policy %q", spec.RestartPolicy)
	}

	switch spec.Type {
	case nodeagentapi.TypeSimple, nodeagentapi.TypeOneshot:
	case "":
		spec.Type = nodeagentapi.TypeSimple
	default:
		return nil, nil, fmt.Errorf("invalid program type %q", spec.Type)
	}

	if spec.Type == nodeagentapi.TypeOneshot && spec.RestartPolicy == nodeagentapi.RestartAlways {
		return nil, nil, fmt.Errorf("oneshot programs cannot use the Always restart policy")
	}

	if err := validateHealthCheck("liveness", spec.LivenessCheck); err != nil {
		return nil, nil, err
	}

	if err := validateHealthCheck("readiness", spec.ReadinessCheck); err != nil {
		return nil, nil, err
	}

	lab, err := uc.repo.GetLab(labID)
	if err != nil {
		return nil, nil, fmt.Errorf("get lab: %w", err)
	}

	nodes, err := uc.repo.ListNodes(labID)
	if err != nil {
		return nil, nil, fmt.Errorf("list nodes: %w", err)
	}

	byID := make(map[string]*model.Node, len(nodes))
	for _, n := range nodes {
		byID[n.Meta.ID] = n
	}

	// The program name is the per-server identity on the agents, so
	// one name may exist at most once per server.
	existing, err := uc.repo.ListPrograms(labID)
	if err != nil {
		return nil, nil, fmt.Errorf("list programs: %w", err)
	}

	taken := make(map[string]bool)
	for _, p := range existing {
		if p.Meta.Name == name {
			taken[p.Spec.ServerName] = true
		}
	}

	var (
		programs []*model.Program
		failures []ServerInstall
	)
	for _, id := range serverIDs {
		server, ok := byID[id]
		switch {
		case !ok:
			failures = append(failures, ServerInstall{ServerID: id, Err: ErrNotFound})

			continue
		case server.Spec.Role != model.RoleServer:
			failures = append(failures, ServerInstall{ServerID: id, ServerName: server.Meta.Name,
				Err: fmt.Errorf("node is a %s; programs run on servers only", server.Spec.Role)})

			continue
		case taken[server.Meta.Name]:
			failures = append(failures, ServerInstall{ServerID: id, ServerName: server.Meta.Name,
				Err: fmt.Errorf("program %q already exists on this server", name)})

			continue
		}

		taken[server.Meta.Name] = true
		p, err := uc.createOne(lab, name, spec, server, pkg, start)
		if err != nil {
			return programs, failures, err
		}

		programs = append(programs, p)
	}

	if len(programs) == 0 && len(failures) > 0 {
		return nil, nil, fmt.Errorf("no program created: %s: %w", failures[0].ServerName, failures[0].Err)
	}

	return programs, failures, nil
}

// createOne persists one program bound to one server and installs it
// on the agent right away when the lab is deployed; an undeployed lab
// gets it at RestorePrograms time. start launches it immediately (on a
// deployed lab) and, for a simple program, records the standing desire
// to run so a later redeploy restores it; a oneshot's start is a
// single run, so its desired state stays stopped.
func (uc *ProgramUsecase) createOne(lab *model.Lab, name string, spec model.ProgramSpec,
	server *model.Node, pkg *model.Package, start bool,
) (*model.Program, error) {
	now := time.Now().UTC()
	p := &model.Program{
		Meta: model.ResourceMeta{
			ID: model.NewID("prog"), Name: name, CreatedAt: now, UpdatedAt: now,
		},
		Spec:   spec,
		Status: model.ProgramStatus{State: model.ProgramStateUnknown},
	}

	p.Spec.LabID = lab.Meta.ID
	p.Spec.ServerID = server.Meta.ID
	p.Spec.ServerName = server.Meta.Name
	p.Spec.Entrypoint = pkg.Spec.Entrypoint
	p.Spec.DesiredState = model.ProgramDesiredStopped
	if start && spec.Type != nodeagentapi.TypeOneshot {
		p.Spec.DesiredState = model.ProgramDesiredRunning
	}

	if err := uc.repo.CreateProgram(p); err != nil {
		return nil, fmt.Errorf("persist program: %w", err)
	}

	if lab.Meta.Generation > 0 {
		p.Status = uc.deployCreated(context.Background(), lab, p, start)
		if err := uc.repo.UpdateProgram(p); err != nil {
			return nil, fmt.Errorf("update program: %w", err)
		}
	}

	return p, nil
}

// agentAddr resolves a server's management address, translating the
// noop-runtime sentinel into actionable guidance: reaching it means
// the controller runs without containerlab (on macOS, outside the
// OrbStack machine) and cannot talk to node agents at all.
func (uc *ProgramUsecase) agentAddr(ctx context.Context, labName, serverName string) (string, error) {
	addr, err := uc.driver.NodeAddress(ctx, labName, serverName)
	if errors.Is(err, runtime.ErrNotSupported) {
		return "", fmt.Errorf("%w; the active runtime cannot manage node agents — "+
			"install containerlab, or on macOS run 'make orb-setup' once and restart "+
			"with 'make down && make up' to deploy inside the OrbStack machine", err)
	}

	if err != nil {
		return "", err
	}

	return addr, nil
}

// deployCreated installs a freshly created program on its agent and,
// when start is set, launches it. Failures are logged and surfaced in
// the returned status without aborting creation.
func (uc *ProgramUsecase) deployCreated(ctx context.Context, lab *model.Lab, p *model.Program, start bool) model.ProgramStatus {
	addr, err := uc.agentAddr(ctx, lab.Meta.Name, p.Spec.ServerName)
	if err != nil {
		uc.log.Warn("resolve agent address", "program", p.Meta.Name, "server", p.Spec.ServerName, "error", err)

		return model.ProgramStatus{State: model.ProgramStateUnknown, LastError: err.Error()}
	}

	if err := uc.ensureOnAgent(ctx, lab.Meta.ID, addr, p); err != nil {
		uc.log.Warn("install program on agent", "program", p.Meta.Name, "server", p.Spec.ServerName, "error", err)

		return model.ProgramStatus{State: model.ProgramStateUnknown, LastError: err.Error()}
	}

	if !start {
		return model.ProgramStatus{State: model.ProgramStateConfigured, LastObserved: time.Now().UTC()}
	}

	status, err := uc.agent.Start(ctx, addr, p.Meta.Name)
	if err != nil {
		uc.log.Warn("start created program", "program", p.Meta.Name, "server", p.Spec.ServerName, "error", err)

		return model.ProgramStatus{State: model.ProgramStateFailed, LastError: err.Error()}
	}

	status.LastObserved = time.Now().UTC()

	return status
}

// NodeInventory reports what is actually on one server, live from
// its agent: installed package versions and every program including
// node-local ones the controller never declared (Managed = false).
func (uc *ProgramUsecase) NodeInventory(ctx context.Context, labID, nodeID string) (*model.NodeInventory, error) {
	server, err := uc.serverNode(labID, nodeID)
	if err != nil {
		return nil, err
	}

	lab, err := uc.repo.GetLab(labID)
	if err != nil {
		return nil, fmt.Errorf("get lab: %w", err)
	}

	if lab.Meta.Generation == 0 {
		return nil, fmt.Errorf("lab %q has no deployed generation; apply a plan first", lab.Meta.Name)
	}

	addr, err := uc.agentAddr(ctx, lab.Meta.Name, server.Meta.Name)
	if err != nil {
		return nil, fmt.Errorf("resolve agent address: %w", err)
	}

	packages, err := uc.agent.ListPackages(ctx, addr)
	if err != nil {
		return nil, fmt.Errorf("agent: %w", err)
	}

	programs, err := uc.agent.ListPrograms(ctx, addr)
	if err != nil {
		return nil, fmt.Errorf("agent: %w", err)
	}

	known, err := uc.repo.ListPrograms(labID)
	if err != nil {
		return nil, fmt.Errorf("list programs: %w", err)
	}

	managed := make(map[string]bool)
	for _, p := range known {
		if p.Spec.ServerName == server.Meta.Name {
			managed[p.Meta.Name] = true
		}
	}

	for i := range programs {
		programs[i].Managed = managed[programs[i].Name]
	}

	return &model.NodeInventory{Packages: packages, Programs: programs}, nil
}

// NodeMetrics samples resource usage of one server live from its
// agent's Prometheus endpoint. The scrape returns counters and
// gauges; rates are diffed against the history collector's latest
// point, so they average over up to one sweep interval. Before the
// collector's first sweep rates stay zero.
func (uc *ProgramUsecase) NodeMetrics(ctx context.Context, labID, nodeID string) (*model.NodeMetrics, error) {
	server, err := uc.serverNode(labID, nodeID)
	if err != nil {
		return nil, err
	}

	lab, err := uc.repo.GetLab(labID)
	if err != nil {
		return nil, fmt.Errorf("get lab: %w", err)
	}

	if lab.Meta.Generation == 0 {
		return nil, fmt.Errorf("lab %q has no deployed generation; apply a plan first", lab.Meta.Name)
	}

	addr, err := uc.agentAddr(ctx, lab.Meta.Name, server.Meta.Name)
	if err != nil {
		return nil, fmt.Errorf("resolve agent address: %w", err)
	}

	m, err := uc.agent.Metrics(ctx, addr)
	if err != nil {
		return nil, fmt.Errorf("agent: %w", err)
	}

	prev, ok := uc.history.Last(labID, server.Meta.Name)
	if ok && m.SampledAt.Sub(prev.Ts) <= metrics.BaselineMaxAge {
		point := model.MetricsPoint{
			Ts:         m.SampledAt,
			CPU:        m.CPU,
			Disk:       m.Disk,
			Interfaces: m.Interfaces,
		}

		metrics.Diff(&point, prev)
		m.CPU, m.Disk, m.Interfaces = point.CPU, point.Disk, point.Interfaces
	}

	return m, nil
}

// NodeMetricsHistory returns the collected resource-usage series of
// one server within [start, end]; a zero end defaults to now, a zero
// start to end minus 30 minutes. Unlike the live views this needs no
// deployed generation: history survives stops and redeploys.
func (uc *ProgramUsecase) NodeMetricsHistory(labID, nodeID string, start, end time.Time) ([]model.MetricsPoint, error) {
	server, err := uc.serverNode(labID, nodeID)
	if err != nil {
		return nil, err
	}

	if end.IsZero() {
		end = time.Now().UTC()
	}

	if start.IsZero() {
		start = end.Add(-historyDefaultRange)
	}

	if !start.Before(end) {
		return nil, fmt.Errorf("invalid range: start %s is not before end %s", start, end)
	}

	return uc.history.Query(labID, server.Meta.Name, start, end), nil
}

// StartProgram launches a program on its server's agent.
func (uc *ProgramUsecase) StartProgram(ctx context.Context, labID, id string) (*model.Program, error) {
	return uc.power(ctx, labID, id, true)
}

// StopProgram stops a program on its server's agent.
func (uc *ProgramUsecase) StopProgram(ctx context.Context, labID, id string) (*model.Program, error) {
	return uc.power(ctx, labID, id, false)
}

func (uc *ProgramUsecase) power(ctx context.Context, labID, id string, on bool) (*model.Program, error) {
	p, addr, err := uc.programAgent(ctx, labID, id)
	if err != nil {
		return nil, err
	}

	// (Re)install first so start works on agents that lost state
	// (fresh container, manual restart); reinstall of a stopped
	// program is idempotent.
	var status model.ProgramStatus
	if on {
		if err := uc.ensureOnAgent(ctx, labID, addr, p); err != nil {
			return nil, err
		}

		status, err = uc.agent.Start(ctx, addr, p.Meta.Name)
		p.Spec.DesiredState = model.ProgramDesiredRunning

		// Starting a oneshot is "run it once" (systemctl start on a
		// Type=oneshot unit), not a standing desire: only auto-start
		// re-runs it on the next boot or redeploy.
		if p.Spec.Type == nodeagentapi.TypeOneshot {
			p.Spec.DesiredState = model.ProgramDesiredStopped
		}
	} else {
		status, err = uc.agent.Stop(ctx, addr, p.Meta.Name)
		p.Spec.DesiredState = model.ProgramDesiredStopped
	}

	if err != nil {
		return nil, fmt.Errorf("agent: %w", err)
	}

	status.LastObserved = time.Now().UTC()
	p.Status = status
	if err := uc.repo.UpdateProgram(p); err != nil {
		return nil, fmt.Errorf("update program: %w", err)
	}

	return p, nil
}

// UpgradeProgram switches a program to another version of its
// package (upgrade or rollback) and restarts it if it was meant to
// run. On an undeployed lab only the reference changes.
func (uc *ProgramUsecase) UpgradeProgram(ctx context.Context, labID, id, version string) (*model.Program, error) {
	p, err := uc.repo.GetProgram(id)
	if err != nil {
		return nil, fmt.Errorf("get program: %w", err)
	}

	if p.Spec.LabID != labID {
		return nil, fmt.Errorf("program %q: %w", id, ErrNotFound)
	}

	if version == p.Spec.PackageVersion {
		return p, nil
	}

	pkg, err := uc.packages.GetPackage(p.Spec.PackageName, version)
	if err != nil {
		return nil, fmt.Errorf("package %s@%s: %w", p.Spec.PackageName, version, err)
	}

	lab, err := uc.repo.GetLab(labID)
	if err != nil {
		return nil, fmt.Errorf("get lab: %w", err)
	}

	uc.log.Info("switching program version", "program", p.Meta.Name,
		"from", p.Spec.PackageVersion, "to", version)
	p.Spec.PackageVersion = version
	p.Spec.Entrypoint = pkg.Spec.Entrypoint

	if lab.Meta.Generation > 0 {
		addr, err := uc.agentAddr(ctx, lab.Meta.Name, p.Spec.ServerName)
		if err != nil {
			return nil, fmt.Errorf("resolve agent address: %w", err)
		}

		// A running instance still executes the old version; stop it
		// before the definition is replaced. Stopping a program the
		// fresh agent never saw fails, which is fine — install below
		// registers it.
		if status, err := uc.agent.Stop(ctx, addr, p.Meta.Name); err == nil {
			p.Status = status
		}

		if err := uc.ensureOnAgent(ctx, labID, addr, p); err != nil {
			return nil, err
		}

		status := model.ProgramStatus{State: model.ProgramStateConfigured}
		if p.Spec.DesiredState == model.ProgramDesiredRunning {
			status, err = uc.agent.Start(ctx, addr, p.Meta.Name)
			if err != nil {
				return nil, fmt.Errorf("start upgraded program: %w", err)
			}
		}

		status.LastObserved = time.Now().UTC()
		p.Status = status
	}

	if err := uc.repo.UpdateProgram(p); err != nil {
		return nil, fmt.Errorf("update program: %w", err)
	}

	return p, nil
}

// DeleteProgram removes a program from its agent (best effort: the
// container may be gone) and from the store.
func (uc *ProgramUsecase) DeleteProgram(ctx context.Context, labID, id string) error {
	p, addr, err := uc.programAgent(ctx, labID, id)
	if err == nil {
		if err := uc.agent.Remove(ctx, addr, p.Meta.Name); err != nil {
			uc.log.Warn("remove program from agent", "program", p.Meta.Name, "error", err)
		}
	} else if p == nil {
		return err
	}

	return uc.repo.DeleteProgram(id)
}

// ProgramOpOutcome is the result of one program in a batch operation;
// Err is nil on success.
type ProgramOpOutcome struct {
	ID   string
	Name string
	Err  error
}

// Batch operation names.
const (
	BatchOpStart  = "start"
	BatchOpStop   = "stop"
	BatchOpDelete = "delete"
)

// BatchProgramOp applies start, stop or delete to several programs of
// a lab. Each id is attempted independently and its outcome recorded,
// so one failure does not abort the rest (the same best-effort shape
// as batch package delivery and creation).
func (uc *ProgramUsecase) BatchProgramOp(ctx context.Context, labID, op string, ids []string) ([]ProgramOpOutcome, error) {
	var fn func(context.Context, string, string) error
	switch op {
	case BatchOpStart:
		fn = func(ctx context.Context, labID, id string) error {
			_, err := uc.StartProgram(ctx, labID, id)

			return err
		}
	case BatchOpStop:
		fn = func(ctx context.Context, labID, id string) error {
			_, err := uc.StopProgram(ctx, labID, id)

			return err
		}
	case BatchOpDelete:
		fn = uc.DeleteProgram
	default:
		return nil, fmt.Errorf("invalid batch op %q (want start, stop or delete)", op)
	}

	if len(ids) == 0 {
		return nil, fmt.Errorf("at least one program is required")
	}

	results := make([]ProgramOpOutcome, 0, len(ids))
	for _, id := range ids {
		outcome := ProgramOpOutcome{ID: id}
		if p, err := uc.repo.GetProgram(id); err == nil {
			outcome.Name = p.Meta.Name
		}

		outcome.Err = fn(ctx, labID, id)
		if outcome.Err != nil {
			uc.log.Warn("batch program op", "op", op, "program", outcome.Name, "id", id, "error", outcome.Err)
		}

		results = append(results, outcome)
	}

	return results, nil
}

// ListPrograms returns the programs of a lab, with their status
// refreshed from the agents (best effort: unreachable agents leave
// the stored status untouched).
func (uc *ProgramUsecase) ListPrograms(ctx context.Context, labID string) ([]*model.Program, error) {
	programs, err := uc.repo.ListPrograms(labID)
	if err != nil {
		return nil, err
	}

	if len(programs) == 0 {
		return programs, nil
	}

	lab, err := uc.repo.GetLab(labID)
	if err != nil {
		return nil, fmt.Errorf("get lab: %w", err)
	}

	if lab.Meta.Generation > 0 {
		uc.refreshStatus(ctx, lab, programs)
	}

	return programs, nil
}

// GetProgramLogs tails a program's combined log from its agent.
func (uc *ProgramUsecase) GetProgramLogs(ctx context.Context, labID, id string, lines int) (string, error) {
	p, addr, err := uc.programAgent(ctx, labID, id)
	if err != nil {
		return "", err
	}

	content, err := uc.agent.TailLogs(ctx, addr, p.Meta.Name, lines)
	if err != nil {
		return "", fmt.Errorf("agent: %w", err)
	}

	return content, nil
}

// RestorePrograms pushes the persisted program desired state back
// onto the (fresh) agents after a deploy: install packages and
// programs, start what is desired running. Called as an apply step.
func (uc *ProgramUsecase) RestorePrograms(ctx context.Context, lab *model.Lab, nodes []*model.Node) error {
	programs, err := uc.repo.ListPrograms(lab.Meta.ID)
	if err != nil {
		return fmt.Errorf("list programs: %w", err)
	}

	if len(programs) == 0 {
		return nil
	}

	// Redeploy is a boot: start programs in StartupOrder (ties by
	// name) so a program's lower-order dependencies are launched
	// before it, matching the agent's own boot sequencing.
	sort.Slice(programs, func(i, j int) bool {
		if programs[i].Spec.StartupOrder != programs[j].Spec.StartupOrder {
			return programs[i].Spec.StartupOrder < programs[j].Spec.StartupOrder
		}

		return programs[i].Meta.Name < programs[j].Meta.Name
	})

	// Every plan rebuilds the topology with fresh node IDs; the
	// stable server identity across generations is its name, so
	// programs re-bind by name and pick up the new ID.
	byName := make(map[string]*model.Node, len(nodes))
	for _, n := range nodes {
		byName[n.Meta.Name] = n
	}

	for _, p := range programs {
		server, ok := byName[p.Spec.ServerName]
		if !ok || server.Spec.Role != model.RoleServer {
			// The plan removed the server; the program is orphaned but
			// kept so the user can see and delete it.
			uc.log.Warn("program server gone after apply", "program", p.Meta.Name, "server", p.Spec.ServerName)

			continue
		}

		p.Spec.ServerID = server.Meta.ID
		if err := uc.restoreOne(ctx, lab, p); err != nil {
			return fmt.Errorf("restore program %s on %s: %w", p.Meta.Name, p.Spec.ServerName, err)
		}

		if err := uc.repo.UpdateProgram(p); err != nil {
			return fmt.Errorf("update program: %w", err)
		}
	}

	return nil
}

// InstallCaptureTool delivers the builtin capture package onto every
// server of the lab, retrying while the freshly booted agents come
// up. Switches need nothing — their image bakes the tool in. A
// missing capture package (dev build without artifacts) only logs:
// the lab works, only server-side capture is unavailable.
func (uc *ProgramUsecase) InstallCaptureTool(ctx context.Context, lab *model.Lab, nodes []*model.Node) error {
	if _, err := uc.packages.GetPackage(model.CapturePackageName, model.CapturePackageVersion); err != nil {
		uc.log.Warn("capture package unavailable; servers get no capture tool", "error", err)

		return nil
	}

	for _, n := range nodes {
		if n.Spec.Role != model.RoleServer {
			continue
		}

		addr, err := uc.agentAddr(ctx, lab.Meta.Name, n.Meta.Name)
		if err != nil {
			return fmt.Errorf("resolve agent address of %s: %w", n.Meta.Name, err)
		}

		pkg, err := uc.agentPackage(ctx, lab.Meta.ID, n.Meta.Name, model.CapturePackageName, model.CapturePackageVersion)
		if err != nil {
			return fmt.Errorf("capture package for %s: %w", n.Meta.Name, err)
		}

		deadline := time.Now().Add(agentRestoreTimeout)
		for {
			err = uc.agent.InstallPackage(ctx, addr, pkg)
			if err == nil {
				break
			}

			if time.Now().After(deadline) {
				return fmt.Errorf("install capture tool on %s: %w", n.Meta.Name, err)
			}

			select {
			case <-ctx.Done():
				return ctx.Err()
			case <-time.After(time.Second):
			}
		}
	}

	return nil
}

// restoreOne installs one program's package and definition (and,
// when desired, starts it), retrying while the exec-started agent is
// still coming up.
func (uc *ProgramUsecase) restoreOne(ctx context.Context, lab *model.Lab, p *model.Program) error {
	addr, err := uc.agentAddr(ctx, lab.Meta.Name, p.Spec.ServerName)
	if err != nil {
		return fmt.Errorf("resolve agent address: %w", err)
	}

	deadline := time.Now().Add(agentRestoreTimeout)
	for {
		err = uc.ensureOnAgent(ctx, lab.Meta.ID, addr, p)
		if err == nil {
			break
		}

		if time.Now().After(deadline) {
			return fmt.Errorf("agent not reachable: %w", err)
		}

		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(time.Second):
		}
	}

	// Boot semantics after a redeploy: what was desired running comes
	// back, and enabled (auto-start) programs start regardless —
	// including oneshots, which re-run on every "boot" like enabled
	// systemd oneshot units.
	status := model.ProgramStatus{State: model.ProgramStateConfigured}
	if p.Spec.DesiredState == model.ProgramDesiredRunning || p.Spec.AutoStart {
		status, err = uc.agent.Start(ctx, addr, p.Meta.Name)
		if err != nil {
			return fmt.Errorf("start: %w", err)
		}
	}

	status.LastObserved = time.Now().UTC()
	p.Status = status

	return nil
}

// ensureOnAgent makes the program runnable on its agent: the package
// version is installed from the controller's repository (a no-op
// when already present), then the program definition is registered.
func (uc *ProgramUsecase) ensureOnAgent(ctx context.Context, labID, addr string, p *model.Program) error {
	pkg, err := uc.agentPackage(ctx, labID, p.Spec.ServerName, p.Spec.PackageName, p.Spec.PackageVersion)
	if err != nil {
		return err
	}

	if err := uc.agent.InstallPackage(ctx, addr, pkg); err != nil {
		return fmt.Errorf("install package %s@%s: %w", pkg.Name, pkg.Version, err)
	}

	if err := uc.agent.Install(ctx, addr, p); err != nil {
		return fmt.Errorf("install program: %w", err)
	}

	return nil
}

// ServerInstall is the per-server outcome of a package delivery.
type ServerInstall struct {
	ServerID   string
	ServerName string
	Err        error
}

// InstallPackageOnServers delivers a package version onto lab
// servers without declaring a program — the "deploy a binary" path
// (apt install without a service unit). Empty serverIDs means every
// server of the lab. Delivery is per-server best effort and
// idempotent: agents skip versions already present with the same
// digest.
func (uc *ProgramUsecase) InstallPackageOnServers(ctx context.Context, labID, name, version string, serverIDs []string) ([]ServerInstall, error) {
	if _, err := uc.packages.GetPackage(name, version); err != nil {
		return nil, fmt.Errorf("package %s@%s: %w", name, version, err)
	}

	lab, err := uc.repo.GetLab(labID)
	if err != nil {
		return nil, fmt.Errorf("get lab: %w", err)
	}

	if lab.Meta.Generation == 0 {
		return nil, fmt.Errorf("lab %q has no deployed generation; apply a plan first", lab.Meta.Name)
	}

	nodes, err := uc.repo.ListNodes(labID)
	if err != nil {
		return nil, fmt.Errorf("list nodes: %w", err)
	}

	servers := make(map[string]*model.Node)
	for _, n := range nodes {
		if n.Spec.Role == model.RoleServer {
			servers[n.Meta.ID] = n
		}
	}

	var targets []*model.Node
	if len(serverIDs) == 0 {
		for _, n := range nodes { // keep the stable node order
			if n.Spec.Role == model.RoleServer {
				targets = append(targets, n)
			}
		}
	} else {
		for _, id := range serverIDs {
			n, ok := servers[id]
			if !ok {
				return nil, fmt.Errorf("server %q: %w", id, ErrNotFound)
			}

			targets = append(targets, n)
		}
	}

	if len(targets) == 0 {
		return nil, fmt.Errorf("lab %q has no servers", lab.Meta.Name)
	}

	results := make([]ServerInstall, 0, len(targets))
	for _, n := range targets {
		err := uc.deliverPackage(ctx, lab, n.Meta.Name, name, version)
		if err != nil {
			uc.log.Warn("install package on server", "package", name+"@"+version,
				"server", n.Meta.Name, "error", err)
		}

		results = append(results, ServerInstall{ServerID: n.Meta.ID, ServerName: n.Meta.Name, Err: err})
	}

	return results, nil
}

// deliverPackage pushes one package version onto one server's agent.
func (uc *ProgramUsecase) deliverPackage(ctx context.Context, lab *model.Lab, serverName, name, version string) error {
	addr, err := uc.agentAddr(ctx, lab.Meta.Name, serverName)
	if err != nil {
		return fmt.Errorf("resolve agent address: %w", err)
	}

	pkg, err := uc.agentPackage(ctx, lab.Meta.ID, serverName, name, version)
	if err != nil {
		return err
	}

	return uc.agent.InstallPackage(ctx, addr, pkg)
}

// agentPackage resolves a package reference into the download
// instruction for one server's agent: the repository URL uses the
// server's management gateway, under which the controller host is
// reachable from inside the container.
func (uc *ProgramUsecase) agentPackage(ctx context.Context, labID, serverName, name, version string) (AgentPackage, error) {
	if uc.repoPort == 0 {
		return AgentPackage{}, fmt.Errorf("package repository is disabled; cannot deliver packages to agents")
	}

	pkg, err := uc.packages.GetPackage(name, version)
	if err != nil {
		return AgentPackage{}, fmt.Errorf("package %s@%s: %w", name, version, err)
	}

	lab, err := uc.repo.GetLab(labID)
	if err != nil {
		return AgentPackage{}, fmt.Errorf("get lab: %w", err)
	}

	gateway, err := uc.driver.NodeGateway(ctx, lab.Meta.Name, serverName)
	if err != nil {
		return AgentPackage{}, fmt.Errorf("resolve management gateway: %w", err)
	}

	return AgentPackage{
		Name:    pkg.Meta.Name,
		Version: pkg.Spec.Version,
		SHA256:  pkg.Status.SHA256,
		URL: fmt.Sprintf("http://%s/packages/%s/%s",
			net.JoinHostPort(gateway, strconv.Itoa(uc.repoPort)), pkg.Meta.Name, pkg.Spec.Version),
		Entrypoint: pkg.Spec.Entrypoint,
		Links:      pkg.Spec.Links,
	}, nil
}

// refreshStatus merges the agents' live program status into the
// stored resources, one agent query per server.
func (uc *ProgramUsecase) refreshStatus(ctx context.Context, lab *model.Lab, programs []*model.Program) {
	byServer := make(map[string][]*model.Program)
	for _, p := range programs {
		byServer[p.Spec.ServerName] = append(byServer[p.Spec.ServerName], p)
	}

	for serverName, group := range byServer {
		addr, err := uc.driver.NodeAddress(ctx, lab.Meta.Name, serverName)
		if err != nil {
			continue
		}

		infos, err := uc.agent.List(ctx, addr)
		if err != nil {
			continue
		}

		for _, p := range group {
			status, ok := infos[p.Meta.Name]
			if !ok {
				// Fresh container without this program (e.g. manual
				// redeploy outside apply): surface it as unknown.
				p.Status.State = model.ProgramStateUnknown

				continue
			}

			status.LastObserved = time.Now().UTC()
			p.Status = status
			if err := uc.repo.UpdateProgram(p); err != nil {
				uc.log.Warn("persist program status", "program", p.Meta.Name, "error", err)
			}
		}
	}
}

// programAgent loads a program of the lab and resolves its agent
// address.
func (uc *ProgramUsecase) programAgent(ctx context.Context, labID, id string) (*model.Program, string, error) {
	p, err := uc.repo.GetProgram(id)
	if err != nil {
		return nil, "", fmt.Errorf("get program: %w", err)
	}

	if p.Spec.LabID != labID {
		return nil, "", fmt.Errorf("program %q: %w", id, ErrNotFound)
	}

	lab, err := uc.repo.GetLab(labID)
	if err != nil {
		return p, "", fmt.Errorf("get lab: %w", err)
	}

	if lab.Meta.Generation == 0 {
		return p, "", fmt.Errorf("lab %q has no deployed generation; apply a plan first", lab.Meta.Name)
	}

	addr, err := uc.agentAddr(ctx, lab.Meta.Name, p.Spec.ServerName)
	if err != nil {
		return p, "", fmt.Errorf("resolve agent address: %w", err)
	}

	return p, addr, nil
}

// serverNode resolves a lab node by ID and requires the server role.
func (uc *ProgramUsecase) serverNode(labID, nodeID string) (*model.Node, error) {
	nodes, err := uc.repo.ListNodes(labID)
	if err != nil {
		return nil, fmt.Errorf("list nodes: %w", err)
	}

	for _, n := range nodes {
		if n.Meta.ID != nodeID {
			continue
		}

		if n.Spec.Role != model.RoleServer {
			return nil, fmt.Errorf("node %q is a %s; programs run on servers only", n.Meta.Name, n.Spec.Role)
		}

		return n, nil
	}

	return nil, fmt.Errorf("server %q: %w", nodeID, ErrNotFound)
}
