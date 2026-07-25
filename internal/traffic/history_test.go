package traffic

import (
	"testing"
	"time"

	"github.com/ifantsai/dcnetlab/internal/model"
)

func TestHistoryQueryAndLast(t *testing.T) {
	h := NewHistory()
	base := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)

	if _, ok := h.Last("s-1"); ok {
		t.Fatal("expected no last point on empty history")
	}

	for i := range 3 {
		h.AppendPoint("s-1", model.TrafficPoint{Ts: base.Add(time.Duration(i) * 5 * time.Second), Rate: float64(i)})
	}

	last, ok := h.Last("s-1")
	if !ok || last.Rate != 2 {
		t.Fatalf("Last = %+v, ok=%v, want rate=2", last, ok)
	}

	points := h.Query("s-1", base, base.Add(20*time.Second))
	if len(points) != 3 {
		t.Fatalf("Query returned %d points, want 3", len(points))
	}

	points = h.Query("s-1", base.Add(6*time.Second), base.Add(20*time.Second))
	if len(points) != 1 {
		t.Fatalf("Query with bound returned %d points, want 1 (only the base+10s point)", len(points))
	}

	if len(h.Query("s-2", base, base.Add(time.Minute))) != 0 {
		t.Error("unrelated scenario should have no points")
	}
}

func TestHistoryTrimsWindow(t *testing.T) {
	h := NewHistory()
	base := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)

	h.AppendPoint("s-1", model.TrafficPoint{Ts: base})
	h.AppendPoint("s-1", model.TrafficPoint{Ts: base.Add(windowDuration + time.Minute)})

	points := h.windows["s-1"]
	if len(points) != 1 {
		t.Fatalf("window = %d points, want 1 (oldest trimmed)", len(points))
	}

	if !points[0].Ts.Equal(base.Add(windowDuration + time.Minute)) {
		t.Errorf("surviving point ts = %v, want the newer one", points[0].Ts)
	}
}

func TestHistoryRetain(t *testing.T) {
	h := NewHistory()
	h.AppendPoint("keep", model.TrafficPoint{Ts: time.Now()})
	h.AppendPoint("drop", model.TrafficPoint{Ts: time.Now()})

	h.Retain(map[string]bool{"keep": true})

	if _, ok := h.Last("keep"); !ok {
		t.Error("kept scenario should still have its window")
	}

	if _, ok := h.Last("drop"); ok {
		t.Error("dropped scenario's window should be gone")
	}
}
