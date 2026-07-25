// Package labapp implements dcnetlab-trafficgen, the built-in test
// application that runs on lab servers as a Program: simple HTTP,
// TCP and UDP servers and clients with structured stat lines on
// stdout, so traffic is observable from the program logs.
package trafficgen

import (
	"context"
	"fmt"
	"log/slog"
)

// maxPayloadBytes bounds --payload-bytes for http/tcp clients, chosen
// to keep the filler buffer and server-side scan buffers small.
const maxPayloadBytes = 1 << 20 // 1 MiB

// maxUDPPayloadBytes additionally bounds UDP, whose payload plus the
// seq prefix must fit in a single datagram and the fixed-size
// server/client read buffer.
const maxUDPPayloadBytes = 60000

// makePayload returns n bytes of filler content, or nil when n is 0;
// it errors when n is negative or exceeds max.
func makePayload(n int, max int) ([]byte, error) {
	if n < 0 {
		return nil, fmt.Errorf("payload-bytes must be >= 0, got %d", n)
	}

	if n > max {
		return nil, fmt.Errorf("payload-bytes must be <= %d, got %d", max, n)
	}

	if n == 0 {
		return nil, nil
	}

	buf := make([]byte, n)
	for i := range buf {
		buf[i] = 'x'
	}

	return buf, nil
}

// Mode runs one trafficgen mode until ctx is cancelled.
type Mode func(ctx context.Context, log *slog.Logger, args []string) error

// Modes maps the mode argument to its implementation.
var Modes = map[string]Mode{
	"http-server": RunHTTPServer,
	"http-client": RunHTTPClient,
	"tcp-server":  RunTCPServer,
	"tcp-client":  RunTCPClient,
	"udp-server":  RunUDPServer,
	"udp-client":  RunUDPClient,
}

// Run dispatches to the named mode.
func Run(ctx context.Context, log *slog.Logger, mode string, args []string) error {
	fn, ok := Modes[mode]
	if !ok {
		return fmt.Errorf("unknown mode %q", mode)
	}

	log.Info("trafficgen starting", "mode", mode, "args", args)

	return fn(ctx, log.With("mode", mode), args)
}
