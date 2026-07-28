package model

import "time"

// FaultTargetKind selects what a fault acts on.
const (
	FaultTargetNode = "node"
	FaultTargetLink = "link"
)

// FaultSide selects which endpoint of a link a fault acts on;
// link-down always acts on both regardless of side.
const (
	FaultSideA    = "a"
	FaultSideB    = "b"
	FaultSideBoth = "both"
)

// FaultType classifies a fault. node-stop/node-restart target a node;
// link-down/interface-down/impairment target a link.
const (
	FaultNodeStop      = "node-stop"
	FaultNodeRestart   = "node-restart"
	FaultLinkDown      = "link-down"
	FaultInterfaceDown = "interface-down"
	FaultImpairment    = "impairment"
)

// FaultTarget identifies what a fault acts on. Node/link names are
// denormalised for display, mirroring TrafficScenarioSpec.
type FaultTarget struct {
	Kind     string `json:"kind"`
	NodeID   string `json:"nodeId,omitempty"`
	NodeName string `json:"nodeName,omitempty"`
	LinkID   string `json:"linkId,omitempty"`
	LinkName string `json:"linkName,omitempty"`
	Side     string `json:"side,omitempty"`
}

// FaultImpairmentSpec shapes egress traffic with one netem qdisc;
// zero fields are omitted from the qdisc, so at least one must be
// set. Combining delay/jitter/loss/rate into one spec mirrors netem
// itself: a network interface carries at most one qdisc, so there is
// nothing to gain from modelling them as separate fault types.
type FaultImpairmentSpec struct {
	DelayMs     int     `json:"delayMs,omitempty"`
	JitterMs    int     `json:"jitterMs,omitempty"`
	LossPercent float64 `json:"lossPercent,omitempty"`
	RateKbit    int     `json:"rateKbit,omitempty"`
}

// FaultScenarioSpec is the desired fault: its target and kind.
type FaultScenarioSpec struct {
	LabID      string               `json:"labId"`
	Target     FaultTarget          `json:"target"`
	Type       string               `json:"type"`
	Impairment *FaultImpairmentSpec `json:"impairment,omitempty"`
}

// FaultScenarioStatus is the observed state: whether the fault is
// currently applied. A target allows at most one applied scenario at
// a time (see FaultUsecase.ApplyFaultScenario), so recovery always
// restores the fixed baseline (interface up, no qdisc, node running)
// rather than a stored snapshot — that baseline is the only state the
// platform ever leaves a target in outside of an applied fault.
type FaultScenarioStatus struct {
	Applied   bool      `json:"applied"`
	AppliedAt time.Time `json:"appliedAt,omitzero"`
	LastError string    `json:"lastError,omitempty"`
}

// FaultScenario injects and recovers a controlled failure against a
// node or a link.
type FaultScenario struct {
	Meta   ResourceMeta        `json:"meta"`
	Spec   FaultScenarioSpec   `json:"spec"`
	Status FaultScenarioStatus `json:"status"`
}
