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
