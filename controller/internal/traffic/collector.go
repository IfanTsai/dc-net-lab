package traffic

import (
	"context"
	"log/slog"
	"sync"
	"time"

	"github.com/ifantsai/dcnetlab/internal/model"
	"github.com/ifantsai/dcnetlab/internal/runtime"
)

// Sweep cadence matches trafficgen's own stats.report interval (see
// nodeapps/internal/trafficgen/stats.go), so each sweep typically
// picks up exactly one new stats line per running scenario.
const sweepInterval = 5 * time.Second

// tailLines caps the log tail read per sweep; a handful of lines
// covers one missed sweep without paying for a full log read.
const tailLines = 5

// Store is the persistence slice the collector needs.
type Store interface {
	ListLabs() ([]*model.Lab, error)
	ListTrafficScenarios(labID string) ([]*model.TrafficScenario, error)
	UpdateTrafficScenario(s *model.TrafficScenario) error
}

// Agent is the node-agent slice the collector needs: tailing the
// client program's log to read trafficgen's periodic stat lines.
type Agent interface {
	TailLogs(ctx context.Context, addr, name string, lines int) (string, error)
}

// Stopper stops a scenario whose configured duration elapsed; biz's
// TrafficUsecase implements it.
type Stopper interface {
	StopTrafficScenario(ctx context.Context, labID, id string) (*model.TrafficScenario, error)
}

// Collector periodically tails every running scenario's client
// program log, diffs trafficgen's cumulative counters into one
// windowed point and feeds it into History plus the scenario's
// assertion results. It runs as a Kratos transport server so its
// lifecycle follows the application's, like the metrics collector.
type Collector struct {
	store   Store
	agent   Agent
	driver  runtime.Driver
	stopper Stopper
	history *History
	log     *slog.Logger

	stop chan struct{}
	done chan struct{}
	now  func() time.Time // injectable for tests

	mu        sync.Mutex
	baselines map[string]statsLine // scenario ID → last processed stats line

	subMu sync.Mutex
	subs  map[string]map[chan struct{}]struct{} // lab ID → sweep tick subscribers
}

// NewCollector wires the traffic collector.
func NewCollector(store Store, agent Agent, driver runtime.Driver, stopper Stopper, history *History, log *slog.Logger) *Collector {
	return &Collector{
		store: store, agent: agent, driver: driver, stopper: stopper, history: history, log: log,
		stop: make(chan struct{}), done: make(chan struct{}), now: time.Now,
		baselines: make(map[string]statsLine),
		subs:      make(map[string]map[chan struct{}]struct{}),
	}
}

// Subscribe delivers a tick after every sweep of one deployed lab, so
// a transport can push the freshly written scenario states; cancel
// must be called to release the subscription. Ticks are conflated: a
// slow consumer sees one tick, not a backlog.
func (c *Collector) Subscribe(labID string) (<-chan struct{}, func()) {
	ch := make(chan struct{}, 1)

	c.subMu.Lock()
	if c.subs[labID] == nil {
		c.subs[labID] = make(map[chan struct{}]struct{})
	}

	c.subs[labID][ch] = struct{}{}
	c.subMu.Unlock()

	return ch, func() {
		c.subMu.Lock()
		delete(c.subs[labID], ch)
		c.subMu.Unlock()
	}
}

// notify wakes the lab's subscribers after a sweep.
func (c *Collector) notify(labID string) {
	c.subMu.Lock()
	defer c.subMu.Unlock()

	for ch := range c.subs[labID] {
		select {
		case ch <- struct{}{}:
		default:
		}
	}
}

// Start runs the sweep loop until Stop; it implements the Kratos
// transport.Server interface.
func (c *Collector) Start(ctx context.Context) error {
	defer close(c.done)

	ticker := time.NewTicker(sweepInterval)
	defer ticker.Stop()

	for {
		select {
		case <-c.stop:
			return nil
		case <-ctx.Done():
			return nil
		case <-ticker.C:
			c.sweep(ctx)
		}
	}
}

// Stop terminates the sweep loop.
func (c *Collector) Stop(ctx context.Context) error {
	close(c.stop)
	select {
	case <-c.done:
	case <-ctx.Done():
	}

	return nil
}

// sweep samples every running scenario of every deployed lab once.
func (c *Collector) sweep(ctx context.Context) {
	labs, err := c.store.ListLabs()
	if err != nil {
		c.log.Error("traffic: list labs", "error", err)

		return
	}

	active := make(map[string]bool)
	for _, lab := range labs {
		if lab.Meta.Generation == 0 {
			continue
		}

		c.sweepLab(ctx, lab, active)
		c.notify(lab.Meta.ID)
	}

	c.history.Retain(active)
	c.retainBaselines(active)
}

// sweepLab samples every running scenario of one lab; expired
// scenarios (Spec.Duration elapsed) are stopped instead of sampled.
func (c *Collector) sweepLab(ctx context.Context, lab *model.Lab, active map[string]bool) {
	scenarios, err := c.store.ListTrafficScenarios(lab.Meta.ID)
	if err != nil {
		c.log.Error("traffic: list scenarios", "lab", lab.Meta.Name, "error", err)

		return
	}

	for _, s := range scenarios {
		active[s.Meta.ID] = true
		if s.Status.Phase != model.TrafficPhaseRunning {
			continue
		}

		if c.expired(s) {
			if _, err := c.stopper.StopTrafficScenario(ctx, lab.Meta.ID, s.Meta.ID); err != nil {
				c.log.Warn("traffic: auto-stop expired scenario", "scenario", s.Meta.Name, "error", err)
			}

			continue
		}

		c.sample(ctx, lab, s)
	}
}

// expired reports whether a bounded-duration scenario's deadline has
// passed; Duration <= 0 means run until stopped.
func (c *Collector) expired(s *model.TrafficScenario) bool {
	if s.Spec.Duration <= 0 || s.Status.StartedAt.IsZero() {
		return false
	}

	return c.now().After(s.Status.StartedAt.Add(s.Spec.Duration))
}

// sample tails the client program's log, diffs the latest stats line
// against the previous sweep's baseline and, when there is new data,
// records a point and re-evaluates the scenario's assertions.
func (c *Collector) sample(ctx context.Context, lab *model.Lab, s *model.TrafficScenario) {
	addr, err := c.driver.NodeAddress(ctx, lab.Meta.Name, s.Spec.SourceServerName)
	if err != nil {
		return
	}

	text, err := c.agent.TailLogs(ctx, addr, s.Status.ClientProgramName, tailLines)
	if err != nil {
		c.log.Debug("traffic: tail client log", "scenario", s.Meta.Name, "error", err)

		return
	}

	line, ok := latestStatsLine(text)
	if !ok {
		return
	}

	prev, hasPrev := c.baseline(s.Meta.ID)
	c.setBaseline(s.Meta.ID, line)
	if !hasPrev || line.Total <= prev.Total {
		return // first sample after (re)start or no new data this sweep: seed only
	}

	totalDelta := line.Total - prev.Total
	successDelta := totalDelta - (line.Failed - prev.Failed)
	point := model.TrafficPoint{
		Ts:          c.now().UTC(),
		Rate:        line.Rate,
		SuccessRate: 100 * float64(successDelta) / float64(totalDelta),
		P50Us:       line.P50Us,
		P95Us:       line.P95Us,
		P99Us:       line.P99Us,
	}

	c.history.AppendPoint(s.Meta.ID, point)

	s.Status.LastPoint = point
	s.Status.Assertions = evaluateAssertions(s.Spec.Assertions, point)
	s.Status.LastObserved = point.Ts
	if err := c.store.UpdateTrafficScenario(s); err != nil {
		c.log.Warn("traffic: persist scenario status", "scenario", s.Meta.Name, "error", err)
	}
}

func (c *Collector) baseline(id string) (statsLine, bool) {
	c.mu.Lock()
	defer c.mu.Unlock()

	l, ok := c.baselines[id]

	return l, ok
}

func (c *Collector) setBaseline(id string, l statsLine) {
	c.mu.Lock()
	defer c.mu.Unlock()

	c.baselines[id] = l
}

func (c *Collector) retainBaselines(active map[string]bool) {
	c.mu.Lock()
	defer c.mu.Unlock()

	for id := range c.baselines {
		if !active[id] {
			delete(c.baselines, id)
		}
	}
}
