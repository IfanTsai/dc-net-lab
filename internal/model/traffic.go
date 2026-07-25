package model

import "time"

// TrafficScenario phases.
const (
	TrafficPhaseStopped = "Stopped"
	TrafficPhaseRunning = "Running"
	TrafficPhaseFailed  = "Failed"
)

// Traffic protocols, matching the trafficgen mode prefixes.
const (
	TrafficProtocolHTTP = "http"
	TrafficProtocolTCP  = "tcp"
	TrafficProtocolUDP  = "udp"
)

// Traffic assertion metrics and comparators.
const (
	TrafficMetricRate        = "rate"
	TrafficMetricSuccessRate = "successRate"
	TrafficMetricP50         = "p50"
	TrafficMetricP95         = "p95"
	TrafficMetricP99         = "p99"

	TrafficComparatorGTE = "gte"
	TrafficComparatorLTE = "lte"
)

// TrafficScenario drives measurable traffic between two lab servers
// using a pair of trafficgen Programs (server role on the
// destination, client role on the source) and reports live
// rate/success-rate/latency metrics against optional assertions.
type TrafficScenario struct {
	Meta   ResourceMeta          `json:"meta"`
	Spec   TrafficScenarioSpec   `json:"spec"`
	Status TrafficScenarioStatus `json:"status"`
}

// TrafficAssertion is one pass/fail threshold evaluated against the
// scenario's latest metrics window.
type TrafficAssertion struct {
	Metric     string  `json:"metric"`
	Comparator string  `json:"comparator"`
	Threshold  float64 `json:"threshold"`
}

// TrafficScenarioSpec is the desired shape of the traffic: which
// servers, which protocol, how hard to drive it.
type TrafficScenarioSpec struct {
	LabID            string `json:"labId"`
	SourceServerID   string `json:"sourceServerId"`
	SourceServerName string `json:"sourceServerName"`
	DestServerID     string `json:"destServerId"`
	DestServerName   string `json:"destServerName"`
	Protocol         string `json:"protocol"`
	// Port defaults per protocol (http 8080, tcp/udp 9000); set it
	// explicitly to run several scenarios of the same protocol against
	// the same destination server without their listeners colliding.
	Port         int                `json:"port,omitempty"`
	Rate         float64            `json:"rate"`        // requests/sec per worker
	Concurrency  int                `json:"concurrency"` // parallel client workers
	PayloadBytes int                `json:"payloadBytes,omitempty"`
	Duration     time.Duration      `json:"duration,omitempty"` // 0 = run until stopped
	Assertions   []TrafficAssertion `json:"assertions,omitempty"`
}

// TrafficPoint is one collected sample of the scenario's traffic:
// the client's windowed rate, success rate and latency percentiles.
type TrafficPoint struct {
	Ts          time.Time `json:"ts"`
	Rate        float64   `json:"rate"`        // requests/sec observed in the window
	SuccessRate float64   `json:"successRate"` // percent, 0-100
	P50Us       int64     `json:"p50Us"`
	P95Us       int64     `json:"p95Us"`
	P99Us       int64     `json:"p99Us"`
}

// TrafficAssertionResult is one assertion evaluated against the
// latest point.
type TrafficAssertionResult struct {
	Metric     string  `json:"metric"`
	Comparator string  `json:"comparator"`
	Threshold  float64 `json:"threshold"`
	Value      float64 `json:"value"`
	Pass       bool    `json:"pass"`
}

// TrafficScenarioStatus is the observed state: which Programs
// implement the scenario and the latest collected metrics.
type TrafficScenarioStatus struct {
	Phase             string                   `json:"phase"`
	ServerProgramID   string                   `json:"serverProgramId,omitempty"`
	ServerProgramName string                   `json:"serverProgramName,omitempty"`
	ClientProgramID   string                   `json:"clientProgramId,omitempty"`
	ClientProgramName string                   `json:"clientProgramName,omitempty"`
	StartedAt         time.Time                `json:"startedAt,omitzero"`
	LastPoint         TrafficPoint             `json:"lastPoint,omitzero"`
	Assertions        []TrafficAssertionResult `json:"assertions,omitempty"`
	LastObserved      time.Time                `json:"lastObserved,omitzero"`
	LastError         string                   `json:"lastError,omitempty"`
}
