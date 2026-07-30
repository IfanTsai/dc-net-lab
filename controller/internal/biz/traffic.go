package biz

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"regexp"
	"strconv"
	"time"

	"github.com/ifantsai/dcnetlab/internal/model"
	"github.com/ifantsai/dcnetlab/internal/nodeagentapi"
)

// trafficNameRE mirrors the agent's program name constraint, capped
// shorter than a program name so appending "-client"/"-server" still
// fits within the agent's 63-character limit.
var trafficNameRE = regexp.MustCompile(`^[a-z0-9][a-z0-9-]{0,54}$`)

// Default listen ports per protocol, matching trafficgen's own
// defaults (nodeapps/internal/trafficgen).
const (
	defaultHTTPPort = 8080
	defaultTCPPort  = 9000
	defaultUDPPort  = 9000
)

// TrafficRepo abstracts the persistence the traffic usecase needs.
type TrafficRepo interface {
	CreateTrafficScenario(s *model.TrafficScenario) error
	UpdateTrafficScenario(s *model.TrafficScenario) error
	DeleteTrafficScenario(id string) error
	GetTrafficScenario(id string) (*model.TrafficScenario, error)
	ListTrafficScenarios(labID string) ([]*model.TrafficScenario, error)
	GetLab(id string) (*model.Lab, error)
	ListNodes(labID string) ([]*model.Node, error)
}

// TrafficHistory is the collected traffic time series; internal/traffic
// implements it.
type TrafficHistory interface {
	Query(scenarioID string, start, end time.Time) []model.TrafficPoint
	Last(scenarioID string) (model.TrafficPoint, bool)
}

// trafficHistoryDefaultRange is the window served when the request
// does not bound the series.
const trafficHistoryDefaultRange = 30 * time.Minute

// TrafficUsecase manages TrafficScenarios: each one drives measurable
// traffic between two lab servers through a pair of trafficgen
// Programs, created and torn down via ProgramUsecase.
type TrafficUsecase struct {
	repo     TrafficRepo
	programs *ProgramUsecase
	history  TrafficHistory
	log      *slog.Logger
}

// NewTrafficUsecase wires the traffic usecase.
func NewTrafficUsecase(repo TrafficRepo, programs *ProgramUsecase, history TrafficHistory, log *slog.Logger) *TrafficUsecase {
	return &TrafficUsecase{repo: repo, programs: programs, history: history, log: log}
}

// CreateTrafficScenario validates and persists one scenario definition
// against two lab servers; it does not start any traffic.
func (uc *TrafficUsecase) CreateTrafficScenario(labID, name string, spec model.TrafficScenarioSpec) (*model.TrafficScenario, error) {
	if !trafficNameRE.MatchString(name) {
		return nil, fmt.Errorf("invalid scenario name %q (lowercase letters, digits, dashes, max 55 chars)", name)
	}

	if err := validateProtocol(spec.Protocol); err != nil {
		return nil, err
	}

	if spec.Rate <= 0 {
		return nil, fmt.Errorf("rate must be > 0")
	}

	if spec.Concurrency < 1 {
		return nil, fmt.Errorf("concurrency must be >= 1")
	}

	if spec.PayloadBytes < 0 {
		return nil, fmt.Errorf("payloadBytes must be >= 0")
	}

	if spec.Duration < 0 {
		return nil, fmt.Errorf("duration must be >= 0")
	}

	for _, a := range spec.Assertions {
		if err := validateAssertion(a); err != nil {
			return nil, err
		}
	}

	nodes, err := uc.repo.ListNodes(labID)
	if err != nil {
		return nil, fmt.Errorf("list nodes: %w", err)
	}

	source, err := serverByID(nodes, spec.SourceServerID)
	if err != nil {
		return nil, fmt.Errorf("source server: %w", err)
	}

	dest, err := serverByID(nodes, spec.DestServerID)
	if err != nil {
		return nil, fmt.Errorf("destination server: %w", err)
	}

	spec.SourceServerName = source.Meta.Name
	spec.DestServerName = dest.Meta.Name

	now := time.Now().UTC()
	s := &model.TrafficScenario{
		Meta: model.ResourceMeta{
			ID: model.NewID("traffic"), Name: name, CreatedAt: now, UpdatedAt: now,
		},
		Spec:   spec,
		Status: model.TrafficScenarioStatus{Phase: model.TrafficPhaseStopped},
	}

	s.Spec.LabID = labID

	if err := uc.repo.CreateTrafficScenario(s); err != nil {
		return nil, fmt.Errorf("persist traffic scenario: %w", err)
	}

	return s, nil
}

// validateProtocol rejects anything but the trafficgen-backed
// protocols.
func validateProtocol(protocol string) error {
	switch protocol {
	case model.TrafficProtocolHTTP, model.TrafficProtocolTCP, model.TrafficProtocolUDP:
		return nil
	default:
		return fmt.Errorf("invalid protocol %q (want http, tcp or udp)", protocol)
	}
}

// validateAssertion rejects an unknown metric or comparator before it
// reaches the collector.
func validateAssertion(a model.TrafficAssertion) error {
	switch a.Metric {
	case model.TrafficMetricRate, model.TrafficMetricSuccessRate,
		model.TrafficMetricP50, model.TrafficMetricP95, model.TrafficMetricP99:
	default:
		return fmt.Errorf("invalid assertion metric %q", a.Metric)
	}

	switch a.Comparator {
	case model.TrafficComparatorGTE, model.TrafficComparatorLTE:
	default:
		return fmt.Errorf("invalid assertion comparator %q (want gte or lte)", a.Comparator)
	}

	return nil
}

// serverByID resolves a node by ID and requires the server role.
func serverByID(nodes []*model.Node, id string) (*model.Node, error) {
	for _, n := range nodes {
		if n.Meta.ID != id {
			continue
		}

		if n.Spec.Role != model.RoleServer {
			return nil, fmt.Errorf("node %q is a %s, not a server", n.Meta.Name, n.Spec.Role)
		}

		return n, nil
	}

	return nil, fmt.Errorf("server %q: %w", id, ErrNotFound)
}

// StartTrafficScenario creates and starts the scenario's server and
// client Programs; already-Running scenarios are left untouched.
// Failure to create the client rolls back a just-created server so no
// orphaned Program is left behind.
func (uc *TrafficUsecase) StartTrafficScenario(ctx context.Context, labID, id string) (*model.TrafficScenario, error) {
	s, err := uc.get(labID, id)
	if err != nil {
		return nil, err
	}

	if s.Status.Phase == model.TrafficPhaseRunning {
		return s, nil
	}

	nodes, err := uc.repo.ListNodes(labID)
	if err != nil {
		return nil, fmt.Errorf("list nodes: %w", err)
	}

	dest, err := serverByID(nodes, s.Spec.DestServerID)
	if err != nil {
		return nil, fmt.Errorf("destination server: %w", err)
	}

	if !dest.Spec.Address.IsValid() {
		return nil, fmt.Errorf("destination server %q has no address yet; deploy the lab first", dest.Meta.Name)
	}

	port := trafficPort(s.Spec.Protocol, s.Spec.Port)
	serverName, clientName := s.Meta.Name+"-server", s.Meta.Name+"-client"

	serverProgram, err := uc.startRole(labID, serverName, serverArgs(s.Spec.Protocol, port), []string{s.Spec.DestServerID})
	if err != nil {
		return nil, fmt.Errorf("start server side: %w", err)
	}

	target := clientTarget(s.Spec.Protocol, dest.Spec.Address.Addr().String(), port)
	clientArgs := clientArgs(s.Spec.Protocol, target, s.Spec.Rate, s.Spec.Concurrency, s.Spec.PayloadBytes)

	clientProgram, err := uc.startRole(labID, clientName, clientArgs, []string{s.Spec.SourceServerID})
	if err != nil {
		if delErr := uc.programs.DeleteProgram(ctx, labID, serverProgram.Meta.ID); delErr != nil {
			uc.log.Warn("traffic: roll back server program after failed start", "scenario", s.Meta.Name, "error", delErr)
		}

		return nil, fmt.Errorf("start client side: %w", err)
	}

	s.Status = model.TrafficScenarioStatus{
		Phase:             model.TrafficPhaseRunning,
		ServerProgramID:   serverProgram.Meta.ID,
		ServerProgramName: serverName,
		ClientProgramID:   clientProgram.Meta.ID,
		ClientProgramName: clientName,
		StartedAt:         time.Now().UTC(),
	}

	if err := uc.repo.UpdateTrafficScenario(s); err != nil {
		return nil, fmt.Errorf("update traffic scenario: %w", err)
	}

	return s, nil
}

// startRole creates (and starts) one program of a scenario on one
// server; restarts always, self-healing crashes for the duration of
// the scenario.
func (uc *TrafficUsecase) startRole(labID, name string, args []string, serverIDs []string) (*model.Program, error) {
	spec := model.ProgramSpec{
		PackageName:    model.BuiltinPackageName,
		PackageVersion: model.BuiltinPackageVersion,
		Args:           args,
		RestartPolicy:  nodeagentapi.RestartAlways,
		Type:           nodeagentapi.TypeSimple,
	}

	programs, failures, err := uc.programs.CreateProgram(labID, name, spec, serverIDs, true)
	if err != nil {
		return nil, err
	}

	if len(programs) == 0 {
		return nil, failures[0].Err
	}

	return programs[0], nil
}

// trafficPort resolves the listen/target port: the spec override, or
// the protocol's trafficgen default.
func trafficPort(protocol string, override int) int {
	if override > 0 {
		return override
	}

	switch protocol {
	case model.TrafficProtocolHTTP:
		return defaultHTTPPort
	default:
		return defaultTCPPort // tcp and udp share the same trafficgen default
	}
}

// trafficgenMode builds the mode argument (os.Args[1]) the trafficgen
// binary dispatches on, e.g. "http-server" or "tcp-client".
func trafficgenMode(protocol, role string) string {
	return protocol + "-" + role
}

// serverArgs builds the trafficgen server-mode invocation: the mode
// argument followed by its flags.
func serverArgs(protocol string, port int) []string {
	return []string{trafficgenMode(protocol, "server"), "--listen", ":" + strconv.Itoa(port)}
}

// clientTarget builds the address a trafficgen client dials: a full
// URL for http, host:port for tcp/udp.
func clientTarget(protocol, destIP string, port int) string {
	if protocol == model.TrafficProtocolHTTP {
		return fmt.Sprintf("http://%s:%d/", destIP, port)
	}

	return fmt.Sprintf("%s:%d", destIP, port)
}

// clientArgs builds the trafficgen client-mode invocation: the mode
// argument followed by its flags; rate maps onto --interval (one
// request per interval per worker).
func clientArgs(protocol, target string, rate float64, concurrency, payloadBytes int) []string {
	args := []string{
		trafficgenMode(protocol, "client"),
		"--target", target,
		"--interval", intervalFor(rate).String(),
		"--concurrency", strconv.Itoa(concurrency),
	}

	if payloadBytes > 0 {
		args = append(args, "--payload-bytes", strconv.Itoa(payloadBytes))
	}

	return args
}

// intervalFor converts a target requests/sec rate into the
// per-worker interval trafficgen clients accept.
func intervalFor(rate float64) time.Duration {
	return time.Duration(float64(time.Second) / rate)
}

// StopTrafficScenario stops the scenario's server and client Programs
// (they are not deleted, so a later Start is instant); already-stopped
// scenarios are left untouched.
func (uc *TrafficUsecase) StopTrafficScenario(ctx context.Context, labID, id string) (*model.TrafficScenario, error) {
	s, err := uc.get(labID, id)
	if err != nil {
		return nil, err
	}

	if s.Status.Phase != model.TrafficPhaseRunning {
		return s, nil
	}

	var errs []error
	if s.Status.ClientProgramID != "" {
		if _, err := uc.programs.StopProgram(ctx, labID, s.Status.ClientProgramID); err != nil {
			errs = append(errs, fmt.Errorf("stop client: %w", err))
		}
	}

	if s.Status.ServerProgramID != "" {
		if _, err := uc.programs.StopProgram(ctx, labID, s.Status.ServerProgramID); err != nil {
			errs = append(errs, fmt.Errorf("stop server: %w", err))
		}
	}

	s.Status.Phase = model.TrafficPhaseStopped
	if len(errs) > 0 {
		s.Status.LastError = errors.Join(errs...).Error()
	} else {
		s.Status.LastError = ""
	}

	if err := uc.repo.UpdateTrafficScenario(s); err != nil {
		return nil, fmt.Errorf("update traffic scenario: %w", err)
	}

	return s, nil
}

// DeleteTrafficScenario stops and removes the scenario's Programs
// (best effort) and the scenario itself.
func (uc *TrafficUsecase) DeleteTrafficScenario(ctx context.Context, labID, id string) error {
	s, err := uc.get(labID, id)
	if err != nil {
		return err
	}

	if s.Status.ClientProgramID != "" {
		if err := uc.programs.DeleteProgram(ctx, labID, s.Status.ClientProgramID); err != nil {
			uc.log.Warn("traffic: delete client program", "scenario", s.Meta.Name, "error", err)
		}
	}

	if s.Status.ServerProgramID != "" {
		if err := uc.programs.DeleteProgram(ctx, labID, s.Status.ServerProgramID); err != nil {
			uc.log.Warn("traffic: delete server program", "scenario", s.Meta.Name, "error", err)
		}
	}

	return uc.repo.DeleteTrafficScenario(id)
}

// ListTrafficScenarios returns the scenarios of a lab.
func (uc *TrafficUsecase) ListTrafficScenarios(labID string) ([]*model.TrafficScenario, error) {
	return uc.repo.ListTrafficScenarios(labID)
}

// TrafficScenarioHistory returns the collected points of one scenario
// within [start, end]; a zero end defaults to now, a zero start to
// end minus 30 minutes.
func (uc *TrafficUsecase) TrafficScenarioHistory(labID, id string, start, end time.Time) ([]model.TrafficPoint, error) {
	s, err := uc.get(labID, id)
	if err != nil {
		return nil, err
	}

	if end.IsZero() {
		end = time.Now().UTC()
	}

	if start.IsZero() {
		start = end.Add(-trafficHistoryDefaultRange)
	}

	if !start.Before(end) {
		return nil, fmt.Errorf("invalid range: start %s is not before end %s", start, end)
	}

	return uc.history.Query(s.Meta.ID, start, end), nil
}

// get loads a scenario of the lab, translating a lab mismatch into
// ErrNotFound like the program usecase does.
func (uc *TrafficUsecase) get(labID, id string) (*model.TrafficScenario, error) {
	s, err := uc.repo.GetTrafficScenario(id)
	if err != nil {
		return nil, fmt.Errorf("get traffic scenario: %w", err)
	}

	if s.Spec.LabID != labID {
		return nil, fmt.Errorf("traffic scenario %q: %w", id, ErrNotFound)
	}

	return s, nil
}
