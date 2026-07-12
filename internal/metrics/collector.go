package metrics

import (
	"context"
	"log/slog"
	"strings"
	"sync"
	"time"

	"github.com/ifantsai/dcnetlab/internal/model"
	"github.com/ifantsai/dcnetlab/internal/runtime"
)

// Store is the persistence slice the collector needs; the data layer
// implements it.
type Store interface {
	ListLabs() ([]*model.Lab, error)
	ListNodes(labID string) ([]*model.Node, error)
}

// Agent is the node-agent slice the collector needs: one Prometheus
// scrape (counters + gauges) per server per sweep.
type Agent interface {
	Metrics(ctx context.Context, addr string) (*model.NodeMetrics, error)
}

// Sweep cadence and baseline policy: rates are counter diffs between
// consecutive sweeps; a baseline older than BaselineMaxAge (collector
// long down, lab stopped) would smear one average over hours, so the
// point is dropped as a gap instead.
const (
	sweepInterval = 15 * time.Second

	// BaselineMaxAge is shared with the realtime view, which diffs
	// its scrape against the collector's latest point.
	BaselineMaxAge = time.Hour
)

// Collector periodically samples every deployed lab's servers through
// their node-agents and feeds the diffed points into History. It runs
// as a Kratos transport server so its lifecycle follows the
// application's, like the observer.
type Collector struct {
	store   Store
	agent   Agent
	driver  runtime.Driver
	history *History
	log     *slog.Logger

	stop chan struct{}
	done chan struct{}
	now  func() time.Time // injectable for tests

	mu        sync.Mutex
	baselines map[string]model.MetricsPoint // labID/server → last counters
}

// NewCollector wires the metrics collector.
func NewCollector(store Store, agent Agent, driver runtime.Driver, history *History, log *slog.Logger) *Collector {
	return &Collector{
		store:     store,
		agent:     agent,
		driver:    driver,
		history:   history,
		log:       log,
		stop:      make(chan struct{}),
		done:      make(chan struct{}),
		now:       time.Now,
		baselines: make(map[string]model.MetricsPoint),
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

// Stop terminates the sweep loop and flushes the open shards.
func (c *Collector) Stop(ctx context.Context) error {
	close(c.stop)
	select {
	case <-c.done:
	case <-ctx.Done():
	}

	c.history.Close()

	return nil
}

// sweep samples every deployed lab once.
func (c *Collector) sweep(ctx context.Context) {
	labs, err := c.store.ListLabs()
	if err != nil {
		c.log.Error("metrics: list labs", "error", err)

		return
	}

	active := make(map[string]bool, len(labs))
	for _, lab := range labs {
		active[lab.Meta.ID] = true
		if collectable(lab) {
			c.sweepLab(ctx, lab)
		}
	}

	c.history.Retain(active)
	c.retainBaselines(active)
}

// collectable mirrors the observer's notion of a lab worth polling.
func collectable(lab *model.Lab) bool {
	if lab.Meta.Generation == 0 {
		return false
	}

	switch lab.Meta.Phase {
	case model.PhaseApplying, model.PhaseDeleting, model.PhaseDeleted:
		return false
	}

	return true
}

// sweepLab samples every deployed server of one lab concurrently and
// appends the sweep. Servers that fail to answer are skipped: the
// series gets a gap, and the (still cumulative) baseline survives for
// the next successful sample.
func (c *Collector) sweepLab(ctx context.Context, lab *model.Lab) {
	nodes, err := c.store.ListNodes(lab.Meta.ID)
	if err != nil {
		c.log.Error("metrics: list nodes", "lab", lab.Meta.Name, "error", err)

		return
	}

	ts := c.now().UTC()

	var mu sync.Mutex
	points := make(map[string]model.MetricsPoint)

	var wg sync.WaitGroup
	for _, n := range nodes {
		if n.Spec.Role != model.RoleServer || n.Meta.ObservedGeneration == 0 {
			continue
		}

		wg.Add(1)
		go func(n *model.Node) {
			defer wg.Done()

			point, ok := c.sampleServer(ctx, lab, n, ts)
			if !ok {
				return
			}

			mu.Lock()
			points[n.Meta.Name] = point
			mu.Unlock()
		}(n)
	}

	wg.Wait()
	c.history.AppendSweep(lab.Meta.ID, lab.Meta.Name, ts, points)
}

// sampleServer takes one counters-only sample and diffs it against
// the server's baseline. The first sample after a (re)start only
// seeds the baseline: without a previous point there is no rate, and
// a zero-rate point would draw a misleading dip.
func (c *Collector) sampleServer(ctx context.Context, lab *model.Lab, n *model.Node, ts time.Time) (model.MetricsPoint, bool) {
	addr, err := c.driver.NodeAddress(ctx, lab.Meta.Name, n.Meta.Name)
	if err != nil {
		return model.MetricsPoint{}, false
	}

	m, err := c.agent.Metrics(ctx, addr)
	if err != nil {
		c.log.Debug("metrics: sample server", "lab", lab.Meta.Name, "server", n.Meta.Name, "error", err)

		return model.MetricsPoint{}, false
	}

	point := model.MetricsPoint{
		Ts:         ts,
		Procs:      m.Procs,
		CPU:        m.CPU,
		Memory:     m.Memory,
		Load:       m.Load,
		Filesystem: m.Filesystem,
		Disk:       m.Disk,
		Interfaces: m.Interfaces,
	}

	key := lab.Meta.ID + "/" + n.Meta.Name
	prev, ok := c.baseline(key)
	if !ok {
		prev, ok = c.history.Last(lab.Meta.ID, n.Meta.Name)
	}

	c.setBaseline(key, point)
	if !ok || ts.Sub(prev.Ts) > BaselineMaxAge || !ts.After(prev.Ts) {
		return model.MetricsPoint{}, false // first sample or stale baseline: gap
	}

	Diff(&point, prev)

	return point, true
}

func (c *Collector) baseline(key string) (model.MetricsPoint, bool) {
	c.mu.Lock()
	defer c.mu.Unlock()

	p, ok := c.baselines[key]

	return p, ok
}

func (c *Collector) setBaseline(key string, p model.MetricsPoint) {
	c.mu.Lock()
	defer c.mu.Unlock()

	c.baselines[key] = p
}

func (c *Collector) retainBaselines(active map[string]bool) {
	c.mu.Lock()
	defer c.mu.Unlock()

	for key := range c.baselines {
		labID, _, _ := strings.Cut(key, "/")
		if !active[labID] {
			delete(c.baselines, key)
		}
	}
}

// Diff fills the rate fields of point from the counter deltas
// against prev. Counters that went backwards (agent or container
// restart) clamp to zero and self-heal on the next sweep. The
// realtime view shares it to diff an on-demand scrape against the
// collector's latest point.
func Diff(point *model.MetricsPoint, prev model.MetricsPoint) {
	dt := point.Ts.Sub(prev.Ts).Seconds()
	if dt <= 0 {
		return
	}

	cpu := &point.CPU
	cpu.UsagePercent = cpuPercent(cpu.UsageSecondsTotal-prev.CPU.UsageSecondsTotal, dt, cpu.LimitCores)
	cpu.UserPercent = cpuPercent(cpu.UserSecondsTotal-prev.CPU.UserSecondsTotal, dt, cpu.LimitCores)
	cpu.SystemPercent = cpuPercent(cpu.SystemSecondsTotal-prev.CPU.SystemSecondsTotal, dt, cpu.LimitCores)

	disk := &point.Disk
	disk.ReadBytesPerSec = counterRate(disk.ReadBytesTotal, prev.Disk.ReadBytesTotal, dt)
	disk.WriteBytesPerSec = counterRate(disk.WriteBytesTotal, prev.Disk.WriteBytesTotal, dt)
	disk.ReadOpsPerSec = counterRate(disk.ReadOpsTotal, prev.Disk.ReadOpsTotal, dt)
	disk.WriteOpsPerSec = counterRate(disk.WriteOpsTotal, prev.Disk.WriteOpsTotal, dt)

	prevIfaces := make(map[string]model.MetricsInterface, len(prev.Interfaces))
	for _, iface := range prev.Interfaces {
		prevIfaces[iface.Name] = iface
	}

	for i := range point.Interfaces {
		iface := &point.Interfaces[i]
		p, seen := prevIfaces[iface.Name]
		if !seen {
			continue // new interface: totals only until the next sweep
		}

		iface.RxBytesPerSec = counterRate(iface.RxBytesTotal, p.RxBytesTotal, dt)
		iface.TxBytesPerSec = counterRate(iface.TxBytesTotal, p.TxBytesTotal, dt)
		iface.RxPacketsPerSec = counterRate(iface.RxPacketsTotal, p.RxPacketsTotal, dt)
		iface.TxPacketsPerSec = counterRate(iface.TxPacketsTotal, p.TxPacketsTotal, dt)
	}
}

// cpuPercent converts a CPU-seconds delta into percent of the limit.
func cpuPercent(delta, dt, limitCores float64) float64 {
	if delta < 0 || limitCores <= 0 {
		return 0
	}

	return 100 * delta / dt / limitCores
}

// counterRate is the per-second rate of one counter delta, clamped at
// zero on wrap.
func counterRate(cur, prev int64, dt float64) float64 {
	if cur < prev {
		return 0
	}

	return float64(cur-prev) / dt
}
