package trafficgen

import (
	"context"
	"log/slog"
	"sort"
	"sync"
	"sync/atomic"
	"time"
)

// statsInterval is how often clients and servers emit a stat line.
const statsInterval = 5 * time.Second

// stats accumulates request counters; clients record latency samples
// for the current window too.
type stats struct {
	total   atomic.Int64
	failed  atomic.Int64
	started time.Time

	mu   sync.Mutex
	lats []int64 // microseconds, samples since the last report
}

func newStats() *stats {
	return &stats{started: time.Now()}
}

// success records one successful operation and its latency.
func (s *stats) success(lat time.Duration) {
	s.total.Add(1)

	s.mu.Lock()
	s.lats = append(s.lats, lat.Microseconds())
	s.mu.Unlock()
}

// failure records one failed operation.
func (s *stats) failure() {
	s.total.Add(1)
	s.failed.Add(1)
}

// hit records one served request (server side, no latency).
func (s *stats) hit() {
	s.total.Add(1)
}

// report emits stat lines every statsInterval until ctx is cancelled,
// with a final line on shutdown. Counters are cumulative; the line
// carries the delta rate and latency percentiles of the last window.
func (s *stats) report(ctx context.Context, log *slog.Logger) {
	ticker := time.NewTicker(statsInterval)
	defer ticker.Stop()

	var lastTotal int64
	emit := func() {
		total, failed := s.total.Load(), s.failed.Load()

		s.mu.Lock()
		lats := s.lats
		s.lats = nil
		s.mu.Unlock()

		attrs := []any{
			"total", total,
			"failed", failed,
			"rate", float64(total-lastTotal) / statsInterval.Seconds(),
			"uptime", time.Since(s.started).Round(time.Second).String(),
		}

		if p50, p95, p99, ok := latencyPercentiles(lats); ok {
			attrs = append(attrs,
				"lat_p50_us", p50,
				"lat_p95_us", p95,
				"lat_p99_us", p99,
			)
		}

		log.Info("stats", attrs...)
		lastTotal = total
	}

	for {
		select {
		case <-ctx.Done():
			emit()

			return
		case <-ticker.C:
			emit()
		}
	}
}

// latencyPercentiles returns the p50/p95/p99 (microseconds) of samples;
// ok is false when samples is empty.
func latencyPercentiles(samples []int64) (p50, p95, p99 int64, ok bool) {
	if len(samples) == 0 {
		return 0, 0, 0, false
	}

	sorted := append([]int64(nil), samples...)
	sort.Slice(sorted, func(i, j int) bool { return sorted[i] < sorted[j] })

	pick := func(p float64) int64 {
		idx := int(p * float64(len(sorted)-1))

		return sorted[idx]
	}

	return pick(0.50), pick(0.95), pick(0.99), true
}
