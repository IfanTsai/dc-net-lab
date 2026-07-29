// Package capture implements the in-container packet capture tool: it
// opens an AF_PACKET socket on one interface, applies the structured
// filter in user space and streams pcapng to stdout. The controller
// starts it via docker exec and owns its lifetime through stdin — EOF
// means stop — so a dropped exec connection can never leave an orphan
// behind. A duration hard cap backs that up.
package capture

import (
	"fmt"
	"time"
)

// Direction selects which side of the interface to record. Frames are
// classified by the kernel packet type: outgoing means sent by this
// node, everything else (host/broadcast/multicast/otherhost) was
// received.
const (
	DirectionBoth = "both"
	DirectionRx   = "rx"
	DirectionTx   = "tx"
)

// linuxPacketOutgoing mirrors unix.PACKET_OUTGOING without importing
// the linux-only constant into portable code.
const linuxPacketOutgoing = 4

// Options configure a capture run. The controller validates user
// input against its own policy limits; the binary only enforces what
// it needs to operate.
type Options struct {
	Iface      string
	SnapLen    int
	Duration   time.Duration
	Direction  string
	MaxPackets uint64
	MaxBytes   uint64
	Filter     Filter
}

// normalise fills defaults and rejects unusable options.
func (o *Options) normalise() error {
	if o.Iface == "" {
		return fmt.Errorf("interface is required")
	}

	if o.SnapLen == 0 {
		o.SnapLen = 256
	}

	if o.SnapLen < 64 || o.SnapLen > 262144 {
		return fmt.Errorf("snap length %d out of range [64, 262144]", o.SnapLen)
	}

	if o.Duration == 0 {
		o.Duration = 30 * time.Second
	}

	if o.Duration < 0 {
		return fmt.Errorf("duration must be positive")
	}

	if o.Direction == "" {
		o.Direction = DirectionBoth
	}

	switch o.Direction {
	case DirectionBoth, DirectionRx, DirectionTx:
	default:
		return fmt.Errorf("unknown direction %q", o.Direction)
	}

	return o.Filter.validate()
}

// directionAllowed reports whether a frame with the given kernel
// packet type passes the direction selector.
func directionAllowed(direction string, pkttype uint8) bool {
	switch direction {
	case DirectionRx:
		return pkttype != linuxPacketOutgoing
	case DirectionTx:
		return pkttype == linuxPacketOutgoing
	default:
		return true
	}
}
