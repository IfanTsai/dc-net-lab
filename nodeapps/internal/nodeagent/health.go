package nodeagent

import (
	"context"
	"fmt"
	"net"
	"net/http"
	"strconv"
	"syscall"
	"time"

	"github.com/ifantsai/dcnetlab/internal/nodeagentapi"
)

// probeHost is the loopback address every probe targets: a program's
// health endpoint is reachable inside its own server container.
const probeHost = "127.0.0.1"

// HealthCheck is the agent-side liveness probe definition. A zero
// value (Type == "") means the program is unmonitored.
type HealthCheck struct {
	Type             string        `json:"type"`
	Port             int           `json:"port,omitempty"`
	Path             string        `json:"path,omitempty"`
	Interval         time.Duration `json:"interval"`
	Timeout          time.Duration `json:"timeout"`
	FailureThreshold int           `json:"failureThreshold"`
}

// defaults for a health check left partially specified.
const (
	defaultProbeInterval = 10 * time.Second
	defaultProbeTimeout  = time.Second
	defaultFailThreshold = 3
	defaultProbeHTTPPath = "/"
	minProbeInterval     = time.Second
)

// normaliseCheck validates and defaults an optional check: nil (or a
// check that normalises to "no check") stays nil, so callers can store
// the result directly.
func normaliseCheck(hc *HealthCheck) (*HealthCheck, error) {
	if hc == nil {
		return nil, nil
	}

	check, err := hc.normalise()
	if err != nil {
		return nil, err
	}

	if check.Type == "" {
		return nil, nil
	}

	return &check, nil
}

// normalise validates the check and fills the defaults; a Type of ""
// means "no check" and returns an empty check unchanged.
func (h HealthCheck) normalise() (HealthCheck, error) {
	if h.Type == "" {
		return HealthCheck{}, nil
	}

	switch h.Type {
	case nodeagentapi.CheckProcess:
	case nodeagentapi.CheckTCP, nodeagentapi.CheckHTTP:
		if h.Port <= 0 || h.Port > 65535 {
			return HealthCheck{}, fmt.Errorf("health check port %d out of range", h.Port)
		}
	default:
		return HealthCheck{}, fmt.Errorf("invalid health check type %q", h.Type)
	}

	if h.Type == nodeagentapi.CheckHTTP && h.Path == "" {
		h.Path = defaultProbeHTTPPath
	}

	if h.Interval <= 0 {
		h.Interval = defaultProbeInterval
	}

	if h.Interval < minProbeInterval {
		h.Interval = minProbeInterval
	}

	if h.Timeout <= 0 {
		h.Timeout = defaultProbeTimeout
	}

	// A probe that can outlast its own interval would pile up; keep it
	// strictly shorter so at most one probe is in flight per tick.
	if h.Timeout >= h.Interval {
		h.Timeout = h.Interval / 2
	}

	if h.FailureThreshold <= 0 {
		h.FailureThreshold = defaultFailThreshold
	}

	return h, nil
}

// probe runs one liveness check against a program's current pid and
// reports whether it passed. A process check succeeds while the
// process group is alive; tcp/http checks target loopback.
func (h HealthCheck) probe(ctx context.Context, pid int) bool {
	ctx, cancel := context.WithTimeout(ctx, h.Timeout)
	defer cancel()

	switch h.Type {
	case nodeagentapi.CheckProcess:
		return processAlive(pid)
	case nodeagentapi.CheckTCP:
		return h.probeTCP(ctx)
	case nodeagentapi.CheckHTTP:
		return h.probeHTTP(ctx)
	default:
		return false
	}
}

// processAlive reports whether pid names a live process. Signal 0
// probes existence without delivering anything: no error or EPERM
// (the process exists but is owned by another user) means alive, only
// ESRCH means gone.
func processAlive(pid int) bool {
	if pid <= 0 {
		return false
	}

	err := syscall.Kill(pid, 0)

	return err == nil || err == syscall.EPERM
}

// probeTCP succeeds when a TCP connection to the loopback port is
// accepted within the timeout.
func (h HealthCheck) probeTCP(ctx context.Context) bool {
	var d net.Dialer
	conn, err := d.DialContext(ctx, "tcp", net.JoinHostPort(probeHost, strconv.Itoa(h.Port)))
	if err != nil {
		return false
	}

	_ = conn.Close()

	return true
}

// probeHTTP succeeds when a GET to the loopback endpoint returns a
// 2xx or 3xx status within the timeout.
func (h HealthCheck) probeHTTP(ctx context.Context) bool {
	url := fmt.Sprintf("http://%s%s", net.JoinHostPort(probeHost, strconv.Itoa(h.Port)), h.Path)
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return false
	}

	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return false
	}

	_ = resp.Body.Close()

	return resp.StatusCode >= 200 && resp.StatusCode < 400
}
