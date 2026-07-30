package server

import (
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/ifantsai/dcnetlab/controller/internal/metrics"
	"github.com/ifantsai/dcnetlab/internal/model"
)

type fakeMetricsSource struct {
	latest []metrics.LabLatest
}

func (s *fakeMetricsSource) Latest() []metrics.LabLatest { return s.latest }

func TestMetricsEndpoint(t *testing.T) {
	now := time.Now().UTC()
	src := &fakeMetricsSource{latest: []metrics.LabLatest{
		{
			Lab: "dc1", Server: "server-1",
			Point: model.MetricsPoint{
				Ts:    now,
				Procs: 4,
				CPU:   model.MetricsCPU{UsagePercent: 12.5, UserSecondsTotal: 100.5},
				Interfaces: []model.MetricsInterface{
					{Name: "eth1", RxBytesTotal: 4096},
				},
			},
		},
		{
			// Stale point (stopped lab): must not be exported.
			Lab: "dc2", Server: "server-9",
			Point: model.MetricsPoint{Ts: now.Add(-5 * time.Minute), Procs: 1},
		},
	}}

	rec := httptest.NewRecorder()
	metricsHandler(src)(rec, httptest.NewRequest("GET", "/metrics", nil))

	if ct := rec.Header().Get("Content-Type"); !strings.HasPrefix(ct, "text/plain; version=0.0.4") {
		t.Errorf("content type = %q", ct)
	}

	body := rec.Body.String()
	for _, want := range []string{
		`dcnetlab_server_cpu_usage_percent{lab="dc1",server="server-1"} 12.5`,
		`dcnetlab_server_cpu_seconds_total{lab="dc1",server="server-1",mode="user"} 100.5`,
		`dcnetlab_server_network_receive_bytes_total{lab="dc1",server="server-1",iface="eth1"} 4096`,
		`dcnetlab_server_procs{lab="dc1",server="server-1"} 4`,
		"# TYPE dcnetlab_server_cpu_usage_percent gauge",
		"# TYPE dcnetlab_server_disk_read_bytes_total counter",
	} {
		if !strings.Contains(body, want) {
			t.Errorf("missing %q in:\n%s", want, body)
		}
	}

	if strings.Contains(body, "server-9") {
		t.Error("stale point exported")
	}
}
