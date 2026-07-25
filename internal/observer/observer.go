// Package observer periodically collects the actual state of deployed
// labs (container state, BGP sessions, routes, interfaces) and feeds
// it back: node/lab phases are reconciled with reality and every
// sweep is broadcast to WebSocket subscribers. It runs as a Kratos
// transport server so its lifecycle follows the application's.
package observer

import (
	"context"
	"errors"
	"log/slog"
	"net"
	"strconv"
	"sync"
	"time"

	"github.com/ifantsai/dcnetlab/internal/agentapi"
	"github.com/ifantsai/dcnetlab/internal/model"
	"github.com/ifantsai/dcnetlab/internal/runtime"
)

// Store is the persistence slice the observer needs; the data layer
// implements it.
type Store interface {
	ListLabs() ([]*model.Lab, error)
	ListNodes(labID string) ([]*model.Node, error)
	ListLinks(labID string) ([]*model.Link, error)
	UpdateNode(n *model.Node) error
	UpdateLab(lab *model.Lab) error
}

// Observation is one node's observed state, as pushed to subscribers.
type Observation struct {
	NodeID          string                  `json:"nodeId"`
	Name            string                  `json:"name"`
	Phase           model.ResourcePhase     `json:"phase"`
	RuntimeState    model.RuntimeState      `json:"runtimeState"`
	BGPEstablished  int                     `json:"bgpEstablished"`
	BGPConfigured   int                     `json:"bgpConfigured"`
	RouteCount      int                     `json:"routeCount"`
	InterfacesUp    int                     `json:"interfacesUp"`
	InterfacesTotal int                     `json:"interfacesTotal"`
	Interfaces      []model.InterfaceStatus `json:"interfaces"`
	VRRPState       string                  `json:"vrrpState"`
	// AgentState is the node-agent reachability of a running server
	// (Up/Down); empty for non-servers or before the first deep sweep.
	AgentState   string    `json:"agentState"`
	LastObserved time.Time `json:"lastObserved"`
}

// Sweep cadence: container state is one cheap docker ps per tick;
// the deep sweep execs into every container, so it runs less often.
const (
	tickInterval      = 2 * time.Second
	deepSweepInterval = 6 * time.Second

	// agentDialTimeout bounds the liveness probe of a server's
	// node-agent port during the deep sweep.
	agentDialTimeout = 500 * time.Millisecond
)

// Observer polls deployed labs and reconciles observed state.
type Observer struct {
	store  Store
	driver runtime.Driver
	log    *slog.Logger

	// agentPort is the node-agent gRPC port probed by the deep sweep;
	// tests override it to point at a local listener.
	agentPort int

	stop chan struct{}
	done chan struct{}

	mu       sync.Mutex
	subs     map[string]map[chan []Observation]struct{}
	latest   map[string][]Observation
	deep     map[string]map[string]deepMetrics // labID → node name → metrics
	agent    map[string]map[string]string      // labID → server name → model.AgentUp/AgentDown
	lastDeep map[string]time.Time
}

// deepMetrics is the exec-collected slice of an observation.
type deepMetrics struct {
	bgpEstablished, bgpConfigured int
	routeCount                    int
	interfaces                    []model.InterfaceStatus
	vrrpState                     string
}

// New wires the observer.
func New(store Store, driver runtime.Driver, log *slog.Logger) *Observer {
	return &Observer{
		store:     store,
		driver:    driver,
		log:       log,
		agentPort: agentapi.DefaultPort,
		stop:      make(chan struct{}),
		done:      make(chan struct{}),
		subs:      make(map[string]map[chan []Observation]struct{}),
		latest:    make(map[string][]Observation),
		deep:      make(map[string]map[string]deepMetrics),
		agent:     make(map[string]map[string]string),
		lastDeep:  make(map[string]time.Time),
	}
}

// Start runs the poll loop until Stop; it implements the Kratos
// transport.Server interface and blocks like a listener would.
func (o *Observer) Start(ctx context.Context) error {
	defer close(o.done)

	ticker := time.NewTicker(tickInterval)
	defer ticker.Stop()

	for {
		select {
		case <-o.stop:
			return nil
		case <-ctx.Done():
			return nil
		case <-ticker.C:
			o.sweep(ctx)
		}
	}
}

// Stop terminates the poll loop.
func (o *Observer) Stop(ctx context.Context) error {
	close(o.stop)
	select {
	case <-o.done:
	case <-ctx.Done():
	}

	return nil
}

// Subscribe delivers every future sweep of one lab; cancel must be
// called to release the subscription. Slow consumers miss sweeps
// rather than blocking the observer.
func (o *Observer) Subscribe(labID string) (<-chan []Observation, func()) {
	ch := make(chan []Observation, 4)

	o.mu.Lock()
	if o.subs[labID] == nil {
		o.subs[labID] = make(map[chan []Observation]struct{})
	}

	o.subs[labID][ch] = struct{}{}
	o.mu.Unlock()

	return ch, func() {
		o.mu.Lock()
		delete(o.subs[labID], ch)
		o.mu.Unlock()
	}
}

// Latest returns the most recent sweep of one lab (nil before the
// first sweep completes).
func (o *Observer) Latest(labID string) []Observation {
	o.mu.Lock()
	defer o.mu.Unlock()

	return o.latest[labID]
}

// sweep observes every deployed lab once.
func (o *Observer) sweep(ctx context.Context) {
	labs, err := o.store.ListLabs()
	if err != nil {
		o.log.Error("observer: list labs", "error", err)

		return
	}

	for _, lab := range labs {
		if !observable(lab) {
			continue
		}

		if err := o.sweepLab(ctx, lab); err != nil {
			if errors.Is(err, runtime.ErrNotSupported) {
				return // noop runtime: nothing to observe at all
			}

			o.log.Error("observer: sweep lab", "lab", lab.Meta.Name, "error", err)
		}
	}
}

// observable reports whether a lab has containers worth polling and
// is not in the middle of a lifecycle transition.
func observable(lab *model.Lab) bool {
	if lab.Meta.Generation == 0 {
		return false
	}

	switch lab.Meta.Phase {
	case model.PhaseApplying, model.PhaseDeleting, model.PhaseDeleted:
		return false
	}

	return true
}

// sweepLab collects one lab: container states every tick, plus the
// exec-based deep metrics when due, then reconciles phases, persists
// changes and broadcasts the result.
func (o *Observer) sweepLab(ctx context.Context, lab *model.Lab) error {
	nodes, err := o.store.ListNodes(lab.Meta.ID)
	if err != nil {
		return err
	}

	// Only nodes that have actually been deployed are observed: a
	// freshly planned (not yet applied) node has no container and
	// must not be flagged as failed.
	deployed := make([]*model.Node, 0, len(nodes))
	names := make([]string, 0, len(nodes))
	for _, n := range nodes {
		if n.Meta.ObservedGeneration > 0 {
			deployed = append(deployed, n)
			names = append(names, n.Meta.Name)
		}
	}

	if len(deployed) == 0 {
		return nil
	}

	states, err := o.driver.NodeStates(ctx, lab.Meta.Name, names)
	if err != nil {
		return err
	}

	if time.Since(o.lastDeepAt(lab.Meta.ID)) >= deepSweepInterval {
		o.collectDeep(ctx, lab, deployed, states)
		o.healAgents(ctx, lab, deployed, states)
	}

	o.reconcile(lab, deployed, states)
	o.broadcast(lab.Meta.ID, deployed)

	return nil
}

func (o *Observer) lastDeepAt(labID string) time.Time {
	o.mu.Lock()
	defer o.mu.Unlock()

	return o.lastDeep[labID]
}

// reconcile syncs node and lab phases with the observed container
// states and persists any node whose observation changed.
func (o *Observer) reconcile(lab *model.Lab, nodes []*model.Node, states map[string]string) {
	o.mu.Lock()
	deep := o.deep[lab.Meta.ID]
	agent := o.agent[lab.Meta.ID]
	o.mu.Unlock()

	running, stopped := 0, 0
	for _, n := range nodes {
		phase, rs := phaseFor(states[n.Meta.Name])
		switch phase {
		case model.PhaseRunning:
			running++
		case model.PhaseStopped:
			stopped++
		}

		next := n.Status
		next.RuntimeState = rs
		if m, ok := deep[n.Meta.Name]; ok {
			next.BGPEstablished = m.bgpEstablished
			next.BGPConfigured = m.bgpConfigured
			next.RouteCount = m.routeCount
			next.Interfaces = m.interfaces
			next.InterfacesUp, next.InterfacesTotal = countUp(m.interfaces)
			next.VRRPState = m.vrrpState
		}

		// Agent reachability only means something for a running server;
		// paused or dead containers fall back to "no verdict".
		if n.Spec.Role == model.RoleServer {
			next.AgentState = agent[n.Meta.Name]
		}

		if n.Meta.Phase == phase && next.Equal(n.Status) {
			continue
		}

		next.LastObserved = time.Now().UTC()
		n.Status = next
		n.Meta.Phase = phase
		if err := o.store.UpdateNode(n); err != nil {
			o.log.Error("observer: update node", "node", n.Meta.Name, "error", err)
		}
	}

	labPhase := model.PhaseDegraded
	switch {
	case running == len(nodes):
		labPhase = model.PhaseRunning
	case stopped == len(nodes):
		labPhase = model.PhaseStopped
	}

	// Transitional lab phases (Planning etc.) are left alone; only
	// steady states are reconciled with device reality.
	switch lab.Meta.Phase {
	case model.PhaseRunning, model.PhaseStopped, model.PhaseDegraded, model.PhaseFailed:
		if lab.Meta.Phase != labPhase {
			lab.Meta.Phase = labPhase
			if err := o.store.UpdateLab(lab); err != nil {
				o.log.Error("observer: update lab", "lab", lab.Meta.Name, "error", err)
			}
		}
	}
}

// healAgents revives dead node-agents: a running server container
// whose agent port stopped accepting connections (agent crash, OOM
// kill, container restart losing the deploy-time exec) gets the boot
// script re-executed. The revived agent restores its auto-start and
// previously running programs from local state, so this sweep is the
// platform's "server boot" path; it shares the script with the
// deploy-time exec so the two cannot drift. Each server's probed
// reachability is recorded for reconcile to surface (a successful
// revive re-probes right away, so it reads Up within the same sweep).
func (o *Observer) healAgents(ctx context.Context, lab *model.Lab, nodes []*model.Node, states map[string]string) {
	reachability := make(map[string]string)
	for _, n := range nodes {
		if n.Spec.Role != model.RoleServer || states[n.Meta.Name] != "running" {
			continue
		}

		reachability[n.Meta.Name] = model.AgentDown
		addr, err := o.driver.NodeAddress(ctx, lab.Meta.Name, n.Meta.Name)
		if err != nil {
			continue
		}

		if o.agentReachable(addr) {
			reachability[n.Meta.Name] = model.AgentUp

			continue
		}

		o.log.Info("observer: reviving node-agent", "lab", lab.Meta.Name, "server", n.Meta.Name)
		if _, err := o.driver.Exec(ctx, lab.Meta.Name, n.Meta.Name, []string{"sh", "-c", agentapi.StartAgentScript}); err != nil {
			o.log.Warn("observer: revive node-agent", "server", n.Meta.Name, "error", err)

			continue
		}

		if o.agentReachable(addr) {
			reachability[n.Meta.Name] = model.AgentUp
		}
	}

	o.mu.Lock()
	o.agent[lab.Meta.ID] = reachability
	o.mu.Unlock()
}

// agentReachable probes the node-agent port of one server.
func (o *Observer) agentReachable(addr string) bool {
	conn, err := net.DialTimeout("tcp", net.JoinHostPort(addr, strconv.Itoa(o.agentPort)), agentDialTimeout)
	if err != nil {
		return false
	}

	_ = conn.Close()

	return true
}

// phaseFor maps a docker container state onto phase and runtime state.
func phaseFor(state string) (model.ResourcePhase, model.RuntimeState) {
	switch state {
	case "running":
		return model.PhaseRunning, model.RuntimeStateRunning
	case "paused":
		return model.PhaseStopped, model.RuntimeStateStopped
	}

	// exited, dead, created, restarting, missing
	return model.PhaseFailed, model.RuntimeStateFailed
}

// broadcast snapshots the lab's observations and fans them out.
func (o *Observer) broadcast(labID string, nodes []*model.Node) {
	obs := make([]Observation, 0, len(nodes))
	now := time.Now().UTC()
	for _, n := range nodes {
		obs = append(obs, Observation{
			NodeID:          n.Meta.ID,
			Name:            n.Meta.Name,
			Phase:           n.Meta.Phase,
			RuntimeState:    n.Status.RuntimeState,
			BGPEstablished:  n.Status.BGPEstablished,
			BGPConfigured:   n.Status.BGPConfigured,
			RouteCount:      n.Status.RouteCount,
			InterfacesUp:    n.Status.InterfacesUp,
			InterfacesTotal: n.Status.InterfacesTotal,
			Interfaces:      n.Status.Interfaces,
			VRRPState:       n.Status.VRRPState,
			AgentState:      n.Status.AgentState,
			LastObserved:    now,
		})
	}

	o.mu.Lock()
	defer o.mu.Unlock()
	o.latest[labID] = obs
	for ch := range o.subs[labID] {
		select {
		case ch <- obs:
		default: // slow consumer: skip this sweep
		}
	}
}
