// Package traffic drives and observes TrafficScenarios: Collector
// polls each running scenario's client Program log for trafficgen's
// periodic stat lines and feeds the parsed points into History, an
// in-memory-only time series (unlike server metrics, traffic
// scenarios are short interactive experiments with no need to
// survive a controller restart).
package traffic

import (
	"sort"
	"sync"
	"time"

	"github.com/ifantsai/dcnetlab/internal/model"
)

// windowDuration bounds how much history one scenario keeps in
// memory; at the 5 s sweep cadence this is 720 points per scenario.
const windowDuration = time.Hour

// History stores each scenario's collected points in a fixed window,
// oldest first. All access is serialised by one mutex: writes happen
// once per sweep and queries are rare UI actions.
type History struct {
	mu      sync.Mutex
	windows map[string][]model.TrafficPoint // scenario ID → points
}

// NewHistory creates an empty in-memory traffic history.
func NewHistory() *History {
	return &History{windows: make(map[string][]model.TrafficPoint)}
}

// AppendPoint records one collected point for a scenario.
func (h *History) AppendPoint(scenarioID string, p model.TrafficPoint) {
	h.mu.Lock()
	defer h.mu.Unlock()

	h.windows[scenarioID] = trimWindow(append(h.windows[scenarioID], p), p.Ts)
}

// trimWindow drops points that fell out of the memory window.
func trimWindow(points []model.TrafficPoint, now time.Time) []model.TrafficPoint {
	cutoff := now.Add(-windowDuration)

	i := 0
	for i < len(points) && points[i].Ts.Before(cutoff) {
		i++
	}

	if i == 0 {
		return points
	}

	return append(points[:0:0], points[i:]...)
}

// Query returns a scenario's points within [start, end].
func (h *History) Query(scenarioID string, start, end time.Time) []model.TrafficPoint {
	h.mu.Lock()
	defer h.mu.Unlock()

	var out []model.TrafficPoint
	for _, p := range h.windows[scenarioID] {
		if p.Ts.Before(start) || p.Ts.After(end) {
			continue
		}

		out = append(out, p)
	}

	return out
}

// Last returns a scenario's most recent point (the collector's diff
// baseline reference point for display purposes).
func (h *History) Last(scenarioID string) (model.TrafficPoint, bool) {
	h.mu.Lock()
	defer h.mu.Unlock()

	window := h.windows[scenarioID]
	if len(window) == 0 {
		return model.TrafficPoint{}, false
	}

	return window[len(window)-1], true
}

// Retain drops the windows of scenarios no longer active (stopped
// scenarios keep their window; deleted ones do not).
func (h *History) Retain(active map[string]bool) {
	h.mu.Lock()
	defer h.mu.Unlock()

	ids := make([]string, 0, len(h.windows))
	for id := range h.windows {
		ids = append(ids, id)
	}

	sort.Strings(ids) // deterministic iteration for tests
	for _, id := range ids {
		if !active[id] {
			delete(h.windows, id)
		}
	}
}
