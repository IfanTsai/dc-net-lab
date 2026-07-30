package metrics

import (
	"io"
	"log/slog"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/ifantsai/dcnetlab/controller/internal/conf"
	"github.com/ifantsai/dcnetlab/internal/model"
)

func testHistory(t *testing.T) (*History, string) {
	t.Helper()

	dir := t.TempDir()
	h := NewHistory(&conf.Data{Dir: dir}, slog.New(slog.NewTextHandler(io.Discard, nil)))
	t.Cleanup(h.Close)

	return h, dir
}

func pointAt(ts time.Time, cpuTotal float64) model.MetricsPoint {
	return model.MetricsPoint{
		Ts:  ts,
		CPU: model.MetricsCPU{UsagePercent: 42, UsageSecondsTotal: cpuTotal, LimitCores: 1},
	}
}

func TestAppendAndQueryWindow(t *testing.T) {
	h, dir := testHistory(t)

	now := time.Now().UTC()
	for i := 0; i < 3; i++ {
		ts := now.Add(time.Duration(i-2) * 15 * time.Second)
		h.AppendSweep("lab-1", "dc1", ts, map[string]model.MetricsPoint{
			"server-1": pointAt(ts, float64(i)),
		})
	}

	points := h.Query("lab-1", "server-1", now.Add(-time.Minute), now)
	if len(points) != 3 {
		t.Fatalf("got %d points, want 3", len(points))
	}

	if points[2].CPU.UsageSecondsTotal != 2 {
		t.Errorf("last point = %+v", points[2])
	}

	// The sweep landed on disk too: one shard, three lines.
	entries, err := os.ReadDir(filepath.Join(dir, "labs", "lab-1", "metrics"))
	if err != nil || len(entries) == 0 {
		t.Fatalf("expected shards, got %v (err %v)", entries, err)
	}

	// Unknown server and empty range stay empty.
	if got := h.Query("lab-1", "nosuch", now.Add(-time.Minute), now); len(got) != 0 {
		t.Errorf("unknown server: %+v", got)
	}
}

func TestReplayAfterRestart(t *testing.T) {
	h, dir := testHistory(t)

	now := time.Now().UTC()
	h.AppendSweep("lab-1", "dc1", now.Add(-30*time.Second), map[string]model.MetricsPoint{
		"server-1": pointAt(now.Add(-30*time.Second), 10),
	})
	h.AppendSweep("lab-1", "dc1", now.Add(-15*time.Second), map[string]model.MetricsPoint{
		"server-1": pointAt(now.Add(-15*time.Second), 20),
	})
	h.Close()

	// A fresh History (controller restart) replays the window and
	// serves the last point as diff baseline.
	h2 := NewHistory(&conf.Data{Dir: dir}, slog.New(slog.NewTextHandler(io.Discard, nil)))
	defer h2.Close()

	last, ok := h2.Last("lab-1", "server-1")
	if !ok || last.CPU.UsageSecondsTotal != 20 {
		t.Fatalf("baseline = %+v ok=%v", last, ok)
	}

	if points := h2.Query("lab-1", "server-1", now.Add(-time.Minute), now); len(points) != 2 {
		t.Errorf("replayed %d points, want 2", len(points))
	}
}

func TestReplaySkipsTornLine(t *testing.T) {
	h, dir := testHistory(t)

	now := time.Now().UTC()
	h.AppendSweep("lab-1", "dc1", now, map[string]model.MetricsPoint{"server-1": pointAt(now, 5)})
	h.Close()

	// Simulate a crash mid-append: a torn trailing line.
	shard := filepath.Join(dir, "labs", "lab-1", "metrics", now.Format(shardHourFmt)+".jsonl")
	f, err := os.OpenFile(shard, os.O_WRONLY|os.O_APPEND, 0o644)
	if err != nil {
		t.Fatal(err)
	}

	if _, err := f.WriteString(`{"v":1,"ts":17838,"server":"server-1","point":{"cp`); err != nil {
		t.Fatal(err)
	}

	_ = f.Close()

	h2 := NewHistory(&conf.Data{Dir: dir}, slog.New(slog.NewTextHandler(io.Discard, nil)))
	defer h2.Close()

	if points := h2.Query("lab-1", "server-1", now.Add(-time.Minute), now); len(points) != 1 {
		t.Errorf("got %d points, want 1 (torn line skipped)", len(points))
	}
}

func TestQueryFromShards(t *testing.T) {
	h, _ := testHistory(t)

	// Points older than the memory window must come back from disk.
	old := time.Now().UTC().Add(-3 * time.Hour)
	h.AppendSweep("lab-1", "dc1", old, map[string]model.MetricsPoint{"server-1": pointAt(old, 7)})

	points := h.Query("lab-1", "server-1", old.Add(-time.Minute), old.Add(time.Minute))
	if len(points) != 1 || points[0].CPU.UsageSecondsTotal != 7 {
		t.Fatalf("disk query = %+v", points)
	}
}

func TestCleanupExpiredShards(t *testing.T) {
	h, dir := testHistory(t)

	metricsDir := filepath.Join(dir, "labs", "lab-1", "metrics")
	if err := os.MkdirAll(metricsDir, 0o755); err != nil {
		t.Fatal(err)
	}

	now := time.Now().UTC()
	expired := now.Add(-26*time.Hour).Format(shardHourFmt) + ".jsonl"
	kept := now.Add(-2*time.Hour).Format(shardHourFmt) + ".jsonl"
	foreign := "notes.txt"
	for _, name := range []string{expired, kept, foreign} {
		if err := os.WriteFile(filepath.Join(metricsDir, name), nil, 0o644); err != nil {
			t.Fatal(err)
		}
	}

	// First touch of the lab runs the cleanup.
	h.AppendSweep("lab-1", "dc1", now, map[string]model.MetricsPoint{"server-1": pointAt(now, 1)})

	if _, err := os.Stat(filepath.Join(metricsDir, expired)); !os.IsNotExist(err) {
		t.Errorf("expired shard survived: %v", err)
	}

	for _, name := range []string{kept, foreign} {
		if _, err := os.Stat(filepath.Join(metricsDir, name)); err != nil {
			t.Errorf("%s should survive: %v", name, err)
		}
	}
}

func TestShardRollover(t *testing.T) {
	h, dir := testHistory(t)

	// Two sweeps in different hours land in different shards. Both
	// stay within retention of the real clock, so cleanup won't prune
	// them away as it would for a shard stamped on a fixed past date.
	t2 := time.Now().UTC()
	t1 := t2.Add(-time.Hour)
	h.AppendSweep("lab-1", "dc1", t1, map[string]model.MetricsPoint{"server-1": pointAt(t1, 1)})
	h.AppendSweep("lab-1", "dc1", t2, map[string]model.MetricsPoint{"server-1": pointAt(t2, 2)})

	for _, hour := range []string{t1.Format(shardHourFmt), t2.Format(shardHourFmt)} {
		if _, err := os.Stat(filepath.Join(dir, "labs", "lab-1", "metrics", hour+".jsonl")); err != nil {
			t.Errorf("shard %s: %v", hour, err)
		}
	}
}

func TestLatestAndRetain(t *testing.T) {
	h, _ := testHistory(t)

	now := time.Now().UTC()
	h.AppendSweep("lab-1", "dc1", now, map[string]model.MetricsPoint{"server-1": pointAt(now, 1)})
	h.AppendSweep("lab-2", "dc2", now, map[string]model.MetricsPoint{"server-9": pointAt(now, 2)})

	latest := h.Latest()
	if len(latest) != 2 || latest[0].Lab != "dc1" || latest[1].Server != "server-9" {
		t.Fatalf("latest = %+v", latest)
	}

	h.Retain(map[string]bool{"lab-1": true})

	latest = h.Latest()
	if len(latest) != 1 || latest[0].Lab != "dc1" {
		t.Errorf("after retain: %+v", latest)
	}
}
