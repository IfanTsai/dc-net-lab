// dcnetlab-trafficgen is the built-in test application for lab servers:
// one binary with HTTP/TCP/UDP server and client modes, structured
// JSON stat lines on stdout. It is installed on servers as a Program
// by dcnetlab-node-agent.
//
// Usage: trafficgen <mode> [flags], e.g.
//
//	trafficgen http-server --listen :8080
//	trafficgen http-client --target http://10.100.0.11:8080/ --interval 500ms
package main

import (
	"context"
	"fmt"
	"log/slog"
	"os"
	"os/signal"
	"sort"
	"syscall"

	"github.com/ifantsai/dcnetlab/serverapps/internal/trafficgen"
)

func main() {
	log := slog.New(slog.NewJSONHandler(os.Stdout, nil))

	if len(os.Args) < 2 {
		fmt.Fprintf(os.Stderr, "usage: %s <mode> [flags]\nmodes: %v\n", os.Args[0], modeNames())
		os.Exit(2)
	}

	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer stop()

	if err := trafficgen.Run(ctx, log, os.Args[1], os.Args[2:]); err != nil {
		log.Error("trafficgen failed", "error", err)
		os.Exit(1)
	}
}

func modeNames() []string {
	names := make([]string, 0, len(trafficgen.Modes))
	for name := range trafficgen.Modes {
		names = append(names, name)
	}

	sort.Strings(names)

	return names
}
