package agent

import (
	"context"
	"net"
	"net/http"
	"net/http/httptest"
	"strconv"
	"testing"
	"time"

	"github.com/ifantsai/dcnetlab/internal/agentapi"
)

func TestHealthCheckNormalise(t *testing.T) {
	tests := []struct {
		name     string
		in       HealthCheck
		wantErr  bool
		wantZero bool // normalises to "no check"
		check    func(t *testing.T, got HealthCheck)
	}{
		{
			name:     "empty type means no check",
			in:       HealthCheck{},
			wantZero: true,
		},
		{
			name: "process defaults",
			in:   HealthCheck{Type: agentapi.CheckProcess},
			check: func(t *testing.T, got HealthCheck) {
				if got.Interval != defaultProbeInterval || got.Timeout != defaultProbeTimeout ||
					got.FailureThreshold != defaultFailThreshold {
					t.Errorf("defaults not applied: %+v", got)
				}
			},
		},
		{
			name: "http fills default path",
			in:   HealthCheck{Type: agentapi.CheckHTTP, Port: 8080},
			check: func(t *testing.T, got HealthCheck) {
				if got.Path != defaultProbeHTTPPath {
					t.Errorf("path = %q, want %q", got.Path, defaultProbeHTTPPath)
				}
			},
		},
		{
			name: "timeout clamped below interval",
			in:   HealthCheck{Type: agentapi.CheckProcess, Interval: 4 * time.Second, Timeout: 9 * time.Second},
			check: func(t *testing.T, got HealthCheck) {
				if got.Timeout >= got.Interval {
					t.Errorf("timeout %s not below interval %s", got.Timeout, got.Interval)
				}
			},
		},
		{name: "bad type", in: HealthCheck{Type: "ping"}, wantErr: true},
		{name: "tcp needs port", in: HealthCheck{Type: agentapi.CheckTCP}, wantErr: true},
		{name: "http port out of range", in: HealthCheck{Type: agentapi.CheckHTTP, Port: 70000}, wantErr: true},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := tt.in.normalise()
			if (err != nil) != tt.wantErr {
				t.Fatalf("err=%v wantErr=%v", err, tt.wantErr)
			}

			if err != nil {
				return
			}

			if tt.wantZero && got.Type != "" {
				t.Fatalf("want no check, got %+v", got)
			}

			if tt.check != nil {
				tt.check(t, got)
			}
		})
	}
}

func TestProbeTCP(t *testing.T) {
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}

	port := ln.Addr().(*net.TCPAddr).Port
	check := HealthCheck{Type: agentapi.CheckTCP, Port: port, Timeout: time.Second}

	if !check.probe(context.Background(), 0) {
		t.Error("probe should pass while the port accepts connections")
	}

	_ = ln.Close()

	if check.probe(context.Background(), 0) {
		t.Error("probe should fail once the port is closed")
	}
}

func TestProbeHTTP(t *testing.T) {
	var status int
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(status)
	}))
	defer srv.Close()

	_, portStr, _ := net.SplitHostPort(srv.Listener.Addr().String())
	port, _ := strconv.Atoi(portStr)
	check := HealthCheck{Type: agentapi.CheckHTTP, Port: port, Path: "/healthz", Timeout: time.Second}

	status = http.StatusOK
	if !check.probe(context.Background(), 0) {
		t.Error("2xx should pass")
	}

	status = http.StatusInternalServerError
	if check.probe(context.Background(), 0) {
		t.Error("5xx should fail")
	}
}

func TestProbeProcess(t *testing.T) {
	check := HealthCheck{Type: agentapi.CheckProcess, Timeout: time.Second}

	if !check.probe(context.Background(), 1) {
		t.Error("pid 1 should be alive")
	}

	if check.probe(context.Background(), 0) {
		t.Error("pid 0 is not a live process")
	}
}

// withLiveness attaches a liveness check to a spec.
func withLiveness(s Spec, hc HealthCheck) Spec {
	s.LivenessCheck = &hc

	return s
}

func TestLivenessMarksHealthy(t *testing.T) {
	m := newTestManager(t)

	// A live long-running process passes the process check, so its
	// health becomes Healthy without any restart.
	spec := withLiveness(shSpec("healthy-svc", "sleep 60", agentapi.RestartNever),
		HealthCheck{Type: agentapi.CheckProcess, Interval: time.Second})
	if _, err := m.Install(spec); err != nil {
		t.Fatal(err)
	}

	if _, err := m.Start("healthy-svc"); err != nil {
		t.Fatal(err)
	}

	info := waitHealth(t, m, "healthy-svc", agentapi.HealthHealthy)
	if info.Restarts != 0 {
		t.Errorf("healthy program restarted %d times", info.Restarts)
	}
}

func TestLivenessRestartsUnhealthy(t *testing.T) {
	m := newTestManager(t)

	// The process stays alive but never listens on the probed port;
	// after the threshold the monitor kills it and OnFailure restarts.
	spec := withLiveness(shSpec("stuck-svc", "sleep 60", agentapi.RestartOnFailure),
		HealthCheck{Type: agentapi.CheckTCP, Port: 1, Interval: time.Second, FailureThreshold: 1})
	if _, err := m.Install(spec); err != nil {
		t.Fatal(err)
	}

	if _, err := m.Start("stuck-svc"); err != nil {
		t.Fatal(err)
	}

	deadline := time.After(10 * time.Second)
	for {
		var restarts int
		for _, info := range m.List() {
			if info.Spec.Name == "stuck-svc" {
				restarts = info.Restarts
			}
		}

		if restarts >= 1 {
			break
		}

		select {
		case <-deadline:
			t.Fatal("unhealthy program was not restarted by the liveness check")
		case <-time.After(50 * time.Millisecond):
		}
	}

	if _, err := m.Stop("stuck-svc"); err != nil {
		t.Fatal(err)
	}
}

func TestReadinessReportsServing(t *testing.T) {
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}

	defer func() { _ = ln.Close() }()

	port := ln.Addr().(*net.TCPAddr).Port
	m := newTestManager(t)

	// The program itself just sleeps; the readiness probe targets a
	// loopback port the test holds open, so it reports ready without
	// ever restarting.
	spec := shSpec("web", "sleep 60", agentapi.RestartNever)
	spec.ReadinessCheck = &HealthCheck{Type: agentapi.CheckTCP, Port: port, Interval: time.Second}
	if _, err := m.Install(spec); err != nil {
		t.Fatal(err)
	}

	info, err := m.Start("web")
	if err != nil {
		t.Fatal(err)
	}

	// A program with a readiness check is not ready until the first
	// successful probe.
	if info.Ready {
		t.Error("should not be ready before the first probe")
	}

	ready := waitReady(t, m, "web")
	if ready.Restarts != 0 {
		t.Errorf("readiness must not restart the program (restarts=%d)", ready.Restarts)
	}

	// Once the port closes, readiness drops without touching the process.
	_ = ln.Close()
	deadline := time.After(5 * time.Second)
	for {
		var last Info
		for _, i := range m.List() {
			if i.Spec.Name == "web" {
				last = i
			}
		}

		if !last.Ready && last.State == agentapi.StateRunning {
			break
		}

		select {
		case <-deadline:
			t.Fatal("readiness did not drop after the port closed")
		case <-time.After(50 * time.Millisecond):
		}
	}
}

func TestNoReadinessCheckIsReadyWhenRunning(t *testing.T) {
	m := newTestManager(t)

	if _, err := m.Install(shSpec("plain", "sleep 60", agentapi.RestartNever)); err != nil {
		t.Fatal(err)
	}

	info, err := m.Start("plain")
	if err != nil {
		t.Fatal(err)
	}

	// No readiness check: ready as soon as it runs.
	if !info.Ready {
		t.Errorf("program without a readiness check should be ready when running: %+v", info)
	}
}

func TestBootStartsInStartupOrder(t *testing.T) {
	dir := t.TempDir()
	m1 := newManagerAt(t, dir)

	// Install three auto-start programs out of order; on the next boot
	// they must start in ascending StartupOrder.
	for _, tc := range []struct {
		name  string
		order int
	}{{"third", 30}, {"first", 10}, {"second", 20}} {
		spec := shSpec(tc.name, "sleep 60", agentapi.RestartNever)
		spec.AutoStart = true
		spec.StartupOrder = tc.order
		if _, err := m1.Install(spec); err != nil {
			t.Fatal(err)
		}
	}

	m1.Shutdown()

	m2, err := NewManager(dir, testLogger())
	if err != nil {
		t.Fatal(err)
	}

	defer m2.Shutdown()

	// Every program comes up; StartedAt reflects the launch order.
	i1 := waitState(t, m2, "first", agentapi.StateRunning)
	i2 := waitState(t, m2, "second", agentapi.StateRunning)
	i3 := waitState(t, m2, "third", agentapi.StateRunning)
	if !i1.StartedAt.Before(i2.StartedAt) || !i2.StartedAt.Before(i3.StartedAt) {
		t.Errorf("boot order not honored: first=%s second=%s third=%s",
			i1.StartedAt, i2.StartedAt, i3.StartedAt)
	}
}

// waitReady polls until the program reports ready.
func waitReady(t *testing.T, m *Manager, name string) Info {
	t.Helper()

	var last Info
	for range 300 {
		for _, info := range m.List() {
			if info.Spec.Name == name {
				last = info
			}
		}

		if last.Ready {
			return last
		}

		time.Sleep(20 * time.Millisecond)
	}

	t.Fatalf("program %s never became ready (%+v)", name, last)

	return last
}

// waitHealth polls until the program reaches the wanted health.
func waitHealth(t *testing.T, m *Manager, name, want string) Info {
	t.Helper()

	var last Info
	for range 300 {
		for _, info := range m.List() {
			if info.Spec.Name == name {
				last = info
			}
		}

		if last.Health == want {
			return last
		}

		time.Sleep(20 * time.Millisecond)
	}

	t.Fatalf("program %s: health %s, want %s (%+v)", name, last.Health, want, last)

	return last
}
