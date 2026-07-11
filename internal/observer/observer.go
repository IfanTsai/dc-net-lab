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
	"sync"
	"time"

	"github.com/ifantsai/dcnetlab/internal/model"
	"github.com/ifantsai/dcnetlab/internal/runtime"
)

// Store is the persistence slice the observer needs; the data layer
// implements it.
type Store interface {
	ListLabs() ([]*model.Lab, error)
	ListNodes(labID string) ([]*model.Node, error)
	UpdateNode(n *model.Node) error
	UpdateLab(lab *model.Lab) error
}

// Observation is one node's observed state, as pushed to subscribers.
type Observation struct {
	NodeID          string              `json:"nodeId"`
	Name            string              `json:"name"`
	Phase           model.ResourcePhase `json:"phase"`
	RuntimeState    model.RuntimeState  `json:"runtimeState"`
	BGPEstablished  int                 `json:"bgpEstablished"`
	BGPConfigured   int                 `json:"bgpConfigured"`
	RouteCount      int                 `json:"routeCount"`
	InterfacesUp    int                 `json:"interfacesUp"`
	InterfacesTotal int                 `json:"interfacesTotal"`
	LastObserved    time.Time           `json:"lastObserved"`
}

// Sweep cadence: container state is one cheap docker ps per tick;
// the deep sweep execs into every container, so it runs less often.
const (
	tickInterval      = 2 * time.Second
	deepSweepInterval = 6 * time.Second
)

// Observer polls deployed labs and reconciles observed state.
type Observer struct {
	store  Store
	driver runtime.Driver
	log    *slog.Logger

	stop chan struct{}
	done chan struct{}

	mu       sync.Mutex
	subs     map[string]map[chan []Observation]struct{}
	latest   map[string][]Observation
	deep     map[string]map[string]deepMetrics // labID → node name → metrics
	lastDeep map[string]time.Time
}

// deepMetrics is the exec-collected slice of an observation.
type deepMetrics struct {
	bgpEstablished, bgpConfigured int
	routeCount                    int
	interfacesUp, interfacesTotal int
}

// New wires the observer.
func New(store Store, driver runtime.Driver, log *slog.Logger) *Observer {
	return &Observer{
		store:    store,
		driver:   driver,
		log:      log,
		stop:     make(chan struct{}),
		done:     make(chan struct{}),
		subs:     make(map[string]map[chan []Observation]struct{}),
		latest:   make(map[string][]Observation),
		deep:     make(map[string]map[string]deepMetrics),
		lastDeep: make(map[string]time.Time),
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
			next.InterfacesUp = m.interfacesUp
			next.InterfacesTotal = m.interfacesTotal
		}

		if n.Meta.Phase == phase && next == n.Status {
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
