package model

import "time"

// Capture directions: which side of the interface to record.
const (
	CaptureDirectionBoth = "both"
	CaptureDirectionRx   = "rx"
	CaptureDirectionTx   = "tx"
)

// Capture protocol filters. bgp and vxlan are conveniences over their
// well-known ports.
const (
	CaptureProtocolARP   = "arp"
	CaptureProtocolICMP  = "icmp"
	CaptureProtocolTCP   = "tcp"
	CaptureProtocolUDP   = "udp"
	CaptureProtocolBGP   = "bgp"
	CaptureProtocolVXLAN = "vxlan"
)

// CaptureSession states. Completed means the capture ended by itself
// (duration or a packet/byte limit reached), Stopped that the user
// ended it; both leave a readable pcapng file behind.
const (
	CaptureStateRunning   = "Running"
	CaptureStateCompleted = "Completed"
	CaptureStateStopped   = "Stopped"
	CaptureStateFailed    = "Failed"
)

// CaptureFilter restricts which frames are recorded; the zero value
// records everything. Prefixes accept CIDR or bare addresses; port
// matches either transport side.
type CaptureFilter struct {
	Protocol  string `json:"protocol,omitempty"`
	SrcPrefix string `json:"srcPrefix,omitempty"`
	DstPrefix string `json:"dstPrefix,omitempty"`
	Port      int    `json:"port,omitempty"`
}

// IsZero reports whether the filter records everything.
func (f CaptureFilter) IsZero() bool {
	return f == CaptureFilter{}
}

// CaptureSessionSpec is the desired capture: where to listen and what
// to keep. Interface must be a modelled interface of the node (a
// topology link endpoint or a logical interface like vlanif/bond0);
// implementation-level interfaces (eth0, br0, macvlan) are not
// capture targets. NodeName is denormalised for display.
type CaptureSessionSpec struct {
	LabID      string        `json:"labId"`
	NodeID     string        `json:"nodeId"`
	NodeName   string        `json:"nodeName,omitempty"`
	Interface  string        `json:"interface"`
	Direction  string        `json:"direction,omitempty"`  // both (default), rx or tx
	SnapLength int           `json:"snapLength,omitempty"` // bytes kept per packet
	Duration   time.Duration `json:"duration,omitempty"`   // hard stop
	MaxPackets uint64        `json:"maxPackets,omitempty"` // 0 = unlimited
	MaxBytes   uint64        `json:"maxBytes,omitempty"`   // captured bytes, 0 = policy default
	Filter     CaptureFilter `json:"filter,omitzero"`
}

// CaptureSessionStatus is the observed state of the capture and its
// running counters.
type CaptureSessionStatus struct {
	State     string    `json:"state"`
	Packets   uint64    `json:"packets"`
	Bytes     uint64    `json:"bytes"` // captured (snapped) bytes
	StartedAt time.Time `json:"startedAt,omitzero"`
	EndedAt   time.Time `json:"endedAt,omitzero"`
	LastError string    `json:"lastError,omitempty"`
}

// CaptureSession records packets on one interface of one lab node
// into a pcapng file, with live metadata streamed to the UI. Sessions
// start capturing on creation and never restart: a finished session
// is a finished recording.
type CaptureSession struct {
	Meta   ResourceMeta         `json:"meta"`
	Spec   CaptureSessionSpec   `json:"spec"`
	Status CaptureSessionStatus `json:"status"`
}
