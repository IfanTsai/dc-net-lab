package trafficgen

import (
	"context"
	"log/slog"
	"sync/atomic"
	"time"
)

// statsInterval is how often clients and servers emit a stat line.
const statsInterval = 5 * time.Second

// stats accumulates request counters; clients record latency too.
type stats struct {
	total   atomic.Int64
	failed  atomic.Int64
	latSum  atomic.Int64 // microseconds
	latMax  atomic.Int64 // microseconds
	started time.Time
}

func newStats() *stats {
	return &stats{started: time.Now()}
}

// success records one successful operation and its latency.
func (s *stats) success(lat time.Duration) {
	s.total.Add(1)
	us := lat.Microseconds()
	s.latSum.Add(us)
	for {
		cur := s.latMax.Load()
		if us <= cur || s.latMax.CompareAndSwap(cur, us) {
			break
		}
	}
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
// carries the delta rate of the last window.
func (s *stats) report(ctx context.Context, log *slog.Logger) {
	ticker := time.NewTicker(statsInterval)
	defer ticker.Stop()

	var lastTotal int64
	emit := func() {
		total, failed := s.total.Load(), s.failed.Load()
		attrs := []any{
			"total", total,
			"failed", failed,
			"rate", float64(total-lastTotal) / statsInterval.Seconds(),
			"uptime", time.Since(s.started).Round(time.Second).String(),
		}

		if ok := total - failed; ok > 0 && s.latSum.Load() > 0 {
			attrs = append(attrs,
				"lat_avg_us", s.latSum.Load()/ok,
				"lat_max_us", s.latMax.Load(),
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
