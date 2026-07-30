// Package metrics collects and stores server resource-usage time
// series. The Collector sweeps every deployed lab's servers through
// their node-agents and diffs cumulative counters into per-interval
// rates (Prometheus rate semantics); History keeps the resulting
// points in a fixed in-memory window for fast queries and appends
// them to hourly JSONL shards for retention across restarts.
package metrics

import (
	"bufio"
	"encoding/json"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/ifantsai/dcnetlab/controller/internal/conf"
	"github.com/ifantsai/dcnetlab/internal/model"
)

// Storage tuning: one point per sweep (15 s), two hours in memory,
// twenty-four hours on disk in hourly shards. Deleting whole shards
// is the only retention mechanism — files are never rewritten.
const (
	windowDuration = 2 * time.Hour
	retention      = 24 * time.Hour
	shardHourFmt   = "2006010215" // UTC hour, also the shard file name
	recordVersion  = 1
)

// record is one JSONL line: one server's sample of one sweep. The
// point carries both gauges (rates already diffed) and cumulative
// counters; the latter let a restarted collector resume diffing
// against the pre-restart baseline.
type record struct {
	V      int                `json:"v"`
	Ts     int64              `json:"ts"`
	Server string             `json:"server"`
	Point  model.MetricsPoint `json:"point"`
}

// History stores the collected series of every lab. All access is
// serialised by one mutex: writes happen once per sweep and queries
// are rare UI actions over small files.
type History struct {
	dir string // <dataDir>/labs
	log *slog.Logger

	mu   sync.Mutex
	labs map[string]*labHistory
}

// labHistory is one lab's state: the display name (Prometheus label),
// per-server windows, the open shard and the collector's diff
// baselines.
type labHistory struct {
	name    string
	windows map[string][]model.MetricsPoint // server → points, oldest first

	file  *os.File
	buf   *bufio.Writer
	shard string // hour the open file belongs to
}

// NewHistory wires the store under the configured data directory.
func NewHistory(c *conf.Data, log *slog.Logger) *History {
	return &History{
		dir:  filepath.Join(c.Dir, "labs"),
		log:  log,
		labs: make(map[string]*labHistory),
	}
}

// metricsDir is the shard directory of one lab.
func (h *History) metricsDir(labID string) string {
	return filepath.Join(h.dir, labID, "metrics")
}

// ensureLab returns the lab state, replaying the recent shards into
// the memory window on first touch (controller restart).
func (h *History) ensureLab(labID, name string) *labHistory {
	lab, ok := h.labs[labID]
	if !ok {
		lab = &labHistory{windows: make(map[string][]model.MetricsPoint)}
		h.labs[labID] = lab
		h.replay(labID, lab)
		h.cleanup(labID)
	}

	if name != "" {
		lab.name = name
	}

	return lab
}

// AppendSweep records one collector sweep: every server's point goes
// into the memory window and onto the current JSONL shard, flushed
// once. Disk failures degrade to memory-only storage.
func (h *History) AppendSweep(labID, labName string, ts time.Time, points map[string]model.MetricsPoint) {
	if len(points) == 0 {
		return
	}

	h.mu.Lock()
	defer h.mu.Unlock()

	lab := h.ensureLab(labID, labName)

	servers := make([]string, 0, len(points))
	for server := range points {
		servers = append(servers, server)
	}

	sort.Strings(servers) // deterministic shard content

	for _, server := range servers {
		p := points[server]
		lab.windows[server] = trimWindow(append(lab.windows[server], p), ts)
	}

	if err := h.persist(labID, lab, ts, servers, points); err != nil {
		h.log.Warn("metrics: persist sweep; keeping memory window only",
			"lab", labID, "error", err)
	}
}

// trimWindow drops points that fell out of the memory window.
func trimWindow(points []model.MetricsPoint, now time.Time) []model.MetricsPoint {
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

// persist appends the sweep to the lab's current shard, rolling over
// (and cleaning up) on hour boundaries.
func (h *History) persist(labID string, lab *labHistory, ts time.Time,
	servers []string, points map[string]model.MetricsPoint,
) error {
	hour := ts.UTC().Format(shardHourFmt)
	if lab.file == nil || lab.shard != hour {
		if err := h.rollover(labID, lab, hour); err != nil {
			return err
		}
	}

	for _, server := range servers {
		line, err := json.Marshal(record{
			V: recordVersion, Ts: ts.Unix(), Server: server, Point: points[server],
		})
		if err != nil {
			return fmt.Errorf("marshal record: %w", err)
		}

		line = append(line, '\n')
		if _, err := lab.buf.Write(line); err != nil {
			return fmt.Errorf("append shard: %w", err)
		}
	}

	// One flush per sweep: a crash loses at most the last interval.
	if err := lab.buf.Flush(); err != nil {
		return fmt.Errorf("flush shard: %w", err)
	}

	return nil
}

// rollover closes the open shard and opens the one for hour, then
// drops shards that fell out of retention.
func (h *History) rollover(labID string, lab *labHistory, hour string) error {
	if lab.file != nil {
		_ = lab.buf.Flush()
		_ = lab.file.Close()
		lab.file, lab.buf = nil, nil
	}

	dir := h.metricsDir(labID)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return fmt.Errorf("create metrics dir: %w", err)
	}

	f, err := os.OpenFile(filepath.Join(dir, hour+".jsonl"), os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o644)
	if err != nil {
		return fmt.Errorf("open shard: %w", err)
	}

	lab.file, lab.buf, lab.shard = f, bufio.NewWriter(f), hour
	h.cleanup(labID)

	return nil
}

// cleanup deletes shards older than the retention window.
func (h *History) cleanup(labID string) {
	cutoff := time.Now().UTC().Add(-retention).Format(shardHourFmt)

	entries, err := os.ReadDir(h.metricsDir(labID))
	if err != nil {
		return
	}

	for _, e := range entries {
		hour, ok := shardHour(e.Name())
		if !ok || hour >= cutoff {
			continue
		}

		if err := os.Remove(filepath.Join(h.metricsDir(labID), e.Name())); err != nil {
			h.log.Warn("metrics: remove expired shard", "lab", labID, "shard", e.Name(), "error", err)
		}
	}
}

// shardHour extracts the hour stamp of a shard file name.
func shardHour(name string) (string, bool) {
	hour, ok := strings.CutSuffix(name, ".jsonl")
	if !ok || len(hour) != len(shardHourFmt) {
		return "", false
	}

	if _, err := time.Parse(shardHourFmt, hour); err != nil {
		return "", false
	}

	return hour, true
}

// replay rebuilds the memory window from the shards covering it, so a
// restarted controller serves history (and diff baselines) without a
// 2 h blackout. Torn or foreign lines are skipped.
func (h *History) replay(labID string, lab *labHistory) {
	now := time.Now().UTC()
	cutoff := now.Add(-windowDuration)

	for _, hour := range shardsSince(cutoff, now) {
		h.replayShard(labID, lab, hour, cutoff)
	}
}

// shardsSince lists the hour stamps covering [from, to].
func shardsSince(from, to time.Time) []string {
	var hours []string
	for t := from.Truncate(time.Hour); !t.After(to); t = t.Add(time.Hour) {
		hours = append(hours, t.Format(shardHourFmt))
	}

	return hours
}

func (h *History) replayShard(labID string, lab *labHistory, hour string, cutoff time.Time) {
	f, err := os.Open(filepath.Join(h.metricsDir(labID), hour+".jsonl"))
	if err != nil {
		return // shard absent: nothing was collected that hour
	}

	defer func() { _ = f.Close() }()

	sc := bufio.NewScanner(f)
	sc.Buffer(make([]byte, 0, 64*1024), 1024*1024)
	for sc.Scan() {
		var rec record
		if err := json.Unmarshal(sc.Bytes(), &rec); err != nil || rec.Server == "" {
			continue // torn last line or foreign content
		}

		if rec.Point.Ts.Before(cutoff) {
			continue
		}

		lab.windows[rec.Server] = append(lab.windows[rec.Server], rec.Point)
	}
}

// Query returns one server's points within [start, end]. Ranges fully
// inside the memory window are served from memory; anything older is
// read from the shards, which always contain the flushed superset.
func (h *History) Query(labID, server string, start, end time.Time) []model.MetricsPoint {
	h.mu.Lock()
	defer h.mu.Unlock()

	lab := h.ensureLab(labID, "")
	if !start.Before(time.Now().UTC().Add(-windowDuration)) {
		return filterPoints(lab.windows[server], start, end)
	}

	var points []model.MetricsPoint
	for _, hour := range shardsSince(start, end) {
		points = append(points, h.queryShard(labID, server, hour, start, end)...)
	}

	return points
}

func (h *History) queryShard(labID, server, hour string, start, end time.Time) []model.MetricsPoint {
	f, err := os.Open(filepath.Join(h.metricsDir(labID), hour+".jsonl"))
	if err != nil {
		return nil
	}

	defer func() { _ = f.Close() }()

	var points []model.MetricsPoint
	sc := bufio.NewScanner(f)
	sc.Buffer(make([]byte, 0, 64*1024), 1024*1024)
	for sc.Scan() {
		var rec record
		if err := json.Unmarshal(sc.Bytes(), &rec); err != nil || rec.Server != server {
			continue
		}

		if rec.Point.Ts.Before(start) || rec.Point.Ts.After(end) {
			continue
		}

		points = append(points, rec.Point)
	}

	return points
}

func filterPoints(points []model.MetricsPoint, start, end time.Time) []model.MetricsPoint {
	var out []model.MetricsPoint
	for _, p := range points {
		if p.Ts.Before(start) || p.Ts.After(end) {
			continue
		}

		out = append(out, p)
	}

	return out
}

// Last returns a server's most recent point (the collector's diff
// baseline after a restart).
func (h *History) Last(labID, server string) (model.MetricsPoint, bool) {
	h.mu.Lock()
	defer h.mu.Unlock()

	window := h.ensureLab(labID, "").windows[server]
	if len(window) == 0 {
		return model.MetricsPoint{}, false
	}

	return window[len(window)-1], true
}

// LabLatest is the freshest point of one server, labelled for the
// Prometheus endpoint.
type LabLatest struct {
	Lab    string
	Server string
	Point  model.MetricsPoint
}

// Latest returns the most recent point of every server of every lab
// the history has seen, sorted by lab then server.
func (h *History) Latest() []LabLatest {
	h.mu.Lock()
	defer h.mu.Unlock()

	var out []LabLatest
	for _, lab := range h.labs {
		for server, window := range lab.windows {
			if len(window) == 0 {
				continue
			}

			out = append(out, LabLatest{Lab: lab.name, Server: server, Point: window[len(window)-1]})
		}
	}

	sort.Slice(out, func(i, j int) bool {
		if out[i].Lab != out[j].Lab {
			return out[i].Lab < out[j].Lab
		}

		return out[i].Server < out[j].Server
	})

	return out
}

// Retain drops the in-memory state of labs no longer active (deleted
// labs); their shard directories vanish with the lab directory.
func (h *History) Retain(active map[string]bool) {
	h.mu.Lock()
	defer h.mu.Unlock()

	for labID, lab := range h.labs {
		if active[labID] {
			continue
		}

		if lab.file != nil {
			_ = lab.buf.Flush()
			_ = lab.file.Close()
		}

		delete(h.labs, labID)
	}
}

// Close flushes and closes every open shard.
func (h *History) Close() {
	h.mu.Lock()
	defer h.mu.Unlock()

	for _, lab := range h.labs {
		if lab.file != nil {
			_ = lab.buf.Flush()
			_ = lab.file.Close()
			lab.file, lab.buf = nil, nil
		}
	}
}
