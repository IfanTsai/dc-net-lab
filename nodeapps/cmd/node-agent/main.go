// dcnetlab-node-agent is the process supervisor inside every lab
// server container: it installs packages from the controller's
// repository, supervises Programs running out of them, serves the
// NodeAgent gRPC API on the management network for the controller
// and exports node-exporter style resource metrics as a Prometheus
// text-format HTTP endpoint (GET /metrics).
package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"log/slog"
	"net"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"

	"google.golang.org/grpc"

	"github.com/ifantsai/dcnetlab/internal/nodeagentapi"
	"github.com/ifantsai/dcnetlab/nodeapps/internal/nodeagent"
	pb "github.com/ifantsai/dcnetlab/pb/nodeagent/v1"
)

func main() {
	listen := flag.String("listen", fmt.Sprintf(":%d", nodeagentapi.DefaultPort), "gRPC listen address")
	metricsListen := flag.String("metrics-listen",
		fmt.Sprintf(":%d", nodeagentapi.DefaultMetricsPort), "Prometheus /metrics listen address (empty to disable)")
	dir := flag.String("dir", "/opt/dcnetlab/run", "state directory (packages, program metas and logs)")
	flag.Parse()

	log := slog.New(slog.NewJSONHandler(os.Stdout, nil))
	if err := run(*listen, *metricsListen, *dir, log); err != nil {
		log.Error("node-agent failed", "error", err)
		os.Exit(1)
	}
}

func run(listen, metricsListen, dir string, log *slog.Logger) error {
	mgr, err := nodeagent.NewManager(dir, log)
	if err != nil {
		return fmt.Errorf("init manager: %w", err)
	}

	ln, err := net.Listen("tcp", listen)
	if err != nil {
		return fmt.Errorf("listen: %w", err)
	}

	srv := grpc.NewServer()
	pb.RegisterNodeAgentServer(srv, nodeagent.NewService(mgr))

	metricsSrv, err := serveMetrics(metricsListen, log)
	if err != nil {
		return err
	}

	sigCh := make(chan os.Signal, 1)
	signal.Notify(sigCh, syscall.SIGINT, syscall.SIGTERM)
	go func() {
		<-sigCh
		srv.GracefulStop()
	}()

	log.Info("node-agent listening", "addr", listen, "metrics", metricsListen, "dir", dir)
	if err := srv.Serve(ln); err != nil {
		return fmt.Errorf("serve: %w", err)
	}

	if metricsSrv != nil {
		ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
		defer cancel()
		_ = metricsSrv.Shutdown(ctx)
	}

	mgr.Shutdown()

	return nil
}

// serveMetrics starts the Prometheus endpoint; a failure there must
// not take down the supervisor, so errors after startup only log.
func serveMetrics(listen string, log *slog.Logger) (*http.Server, error) {
	if listen == "" {
		return nil, nil
	}

	mux := http.NewServeMux()
	mux.HandleFunc("/metrics", nodeagent.MetricsHandler(nodeagent.NewMetricsCollector()))

	srv := &http.Server{Addr: listen, Handler: mux, ReadHeaderTimeout: 5 * time.Second}
	ln, err := net.Listen("tcp", listen)
	if err != nil {
		return nil, fmt.Errorf("listen metrics: %w", err)
	}

	go func() {
		if err := srv.Serve(ln); err != nil && !errors.Is(err, http.ErrServerClosed) {
			log.Error("metrics endpoint failed", "error", err)
		}
	}()

	return srv, nil
}
