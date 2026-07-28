// Command controller runs the DCNetLab controller as a Kratos
// application: the protobuf-defined API over HTTP (and gRPC),
// resource store, planner and runtime driver in one modular monolith.
// The object graph is assembled by wire from the per-layer
// ProviderSets (see wire.go / wire_gen.go).
package main

import (
	"flag"
	"log/slog"
	"os"
	"path/filepath"

	"github.com/go-kratos/kratos/v2"
	klog "github.com/go-kratos/kratos/v2/log"
	"github.com/go-kratos/kratos/v2/transport"
	kgrpc "github.com/go-kratos/kratos/v2/transport/grpc"
	khttp "github.com/go-kratos/kratos/v2/transport/http"

	"github.com/ifantsai/dcnetlab/internal/conf"
	"github.com/ifantsai/dcnetlab/internal/metrics"
	"github.com/ifantsai/dcnetlab/internal/observer"
	"github.com/ifantsai/dcnetlab/internal/server"
	"github.com/ifantsai/dcnetlab/internal/traffic"
)

func main() {
	var (
		sc conf.Server
		dc conf.Data
	)
	// Default to localhost only, per the security requirements.
	flag.StringVar(&sc.HTTPAddr, "listen", "127.0.0.1:8080", "HTTP address to listen on")
	flag.StringVar(&sc.GRPCAddr, "grpc-listen", "127.0.0.1:9090", "gRPC address to listen on (empty to disable)")
	flag.StringVar(&sc.WebDir, "web-dir", "", "serve the built web UI from this directory (optional)")
	flag.StringVar(&sc.WebDevProxy, "web-dev-proxy", "",
		"proxy web UI requests to this Vite dev server URL instead of serving web-dir (hot reload)")
	// The package repository must be reachable from lab containers, so
	// unlike the API it binds all interfaces (read-only content).
	flag.StringVar(&sc.RepoAddr, "repo-listen", "0.0.0.0:50062", "package repository address for lab servers (empty to disable)")
	flag.StringVar(&dc.Dir, "data-dir", "data", "directory for the database and artifacts")
	flag.StringVar(&dc.Runtime, "runtime", "auto", "runtime driver: containerlab, noop or auto")
	flag.StringVar(&dc.BinDir, "bin-dir", "",
		"host directory with the dcnetlab-node-agent and dcnetlab-node-cli binaries (default: the controller binary's directory)")
	flag.Parse()

	log := slog.New(slog.NewTextHandler(os.Stderr, nil))
	dc.BinDir = resolveBinDir(dc.BinDir, log)
	app, cleanup, err := wireApp(&sc, &dc, server.SlogLogger{S: log}, log)
	if err != nil {
		log.Error("controller init failed", "error", err)
		os.Exit(1)
	}

	defer cleanup()

	log.Info("controller starting", "http", sc.HTTPAddr, "grpc", sc.GRPCAddr, "data_dir", dc.Dir)
	if err := app.Run(); err != nil {
		log.Error("controller exited", "error", err)
		os.Exit(1)
	}
}

// resolveBinDir turns the --bin-dir flag into the absolute host path
// server containers bind-mount for the agent binaries; it defaults
// to the controller binary's own directory (make build puts all
// three binaries side by side).
func resolveBinDir(dir string, log *slog.Logger) string {
	if dir == "" {
		exe, err := os.Executable()
		if err != nil {
			log.Warn("cannot locate controller binary; server agent disabled", "error", err)

			return ""
		}

		dir = filepath.Dir(exe)
	}

	abs, err := filepath.Abs(dir)
	if err != nil {
		log.Warn("cannot resolve bin dir; server agent disabled", "dir", dir, "error", err)

		return ""
	}

	return abs
}

// newApp registers the transport servers with the Kratos application.
// The gRPC server is always constructed but only started when a gRPC
// listen address is configured. The observer and the metrics and
// traffic collectors run as transport servers too, so their poll
// loops follow the app lifecycle.
func newApp(c *conf.Server, logger klog.Logger, hs *khttp.Server, gs *kgrpc.Server, rs *server.RepoServer, obs *observer.Observer, mc *metrics.Collector, tc *traffic.Collector) *kratos.App {
	servers := []transport.Server{hs, rs, obs, mc, tc}
	if c.GRPCAddr != "" {
		servers = append(servers, gs)
	}

	return kratos.New(
		kratos.Name("dcnetlab-controller"),
		kratos.Logger(logger),
		kratos.Server(servers...),
	)
}
