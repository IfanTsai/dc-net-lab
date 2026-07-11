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

	"github.com/go-kratos/kratos/v2"
	klog "github.com/go-kratos/kratos/v2/log"
	"github.com/go-kratos/kratos/v2/transport"
	kgrpc "github.com/go-kratos/kratos/v2/transport/grpc"
	khttp "github.com/go-kratos/kratos/v2/transport/http"

	"github.com/ifantsai/dcnetlab/internal/conf"
	"github.com/ifantsai/dcnetlab/internal/observer"
	"github.com/ifantsai/dcnetlab/internal/server"
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
	flag.StringVar(&dc.Dir, "data-dir", "data", "directory for the database and artifacts")
	flag.StringVar(&dc.Runtime, "runtime", "auto", "runtime driver: containerlab, noop or auto")
	flag.Parse()

	log := slog.New(slog.NewTextHandler(os.Stderr, nil))
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

// newApp registers the transport servers with the Kratos application.
// The gRPC server is always constructed but only started when a gRPC
// listen address is configured. The observer runs as a transport
// server too, so its poll loop follows the app lifecycle.
func newApp(c *conf.Server, logger klog.Logger, hs *khttp.Server, gs *kgrpc.Server, obs *observer.Observer) *kratos.App {
	servers := []transport.Server{hs, obs}
	if c.GRPCAddr != "" {
		servers = append(servers, gs)
	}

	return kratos.New(
		kratos.Name("dcnetlab-controller"),
		kratos.Logger(logger),
		kratos.Server(servers...),
	)
}
