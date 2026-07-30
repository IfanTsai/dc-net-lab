// Command agent runs the DCNetLab data plane daemon on a machine
// hosting lab containers. It serves the Agent gRPC API — deploys,
// in-container execs, faults and the management-network dial proxy —
// backed by the local docker and containerlab CLIs. On a
// single-machine install it shares the host with the controller and
// listens on localhost; on a dedicated data plane machine it listens
// on a routable address and optionally relays package repository
// traffic to the controller (--repo-upstream).
package main

import (
	"context"
	"flag"
	"log/slog"
	"net"
	"os"
	"os/signal"
	"syscall"

	"google.golang.org/grpc"

	"github.com/ifantsai/dcnetlab/agent/internal/clab"
	"github.com/ifantsai/dcnetlab/agent/internal/server"
	"github.com/ifantsai/dcnetlab/internal/runtime"
	pb "github.com/ifantsai/dcnetlab/pb/agent/v1"
)

func main() {
	var (
		listen       string
		dataDir      string
		repoUpstream string
		repoListen   string
	)
	flag.StringVar(&listen, "listen", runtime.DefaultAgentAddr, "gRPC address to listen on (bind a routable address for a dedicated data plane machine)")
	flag.StringVar(&dataDir, "data-dir", "agent-data", "directory for materialised deploy artifacts")
	flag.StringVar(&repoUpstream, "repo-upstream", "",
		"controller package repository address to relay container pulls to (only needed when the controller runs on another machine)")
	flag.StringVar(&repoListen, "repo-listen", "0.0.0.0:50062", "listen address for the package repository relay")
	flag.Parse()

	log := slog.New(slog.NewTextHandler(os.Stderr, nil))
	driver := &clab.ContainerlabDriver{}
	if !driver.Available() {
		log.Warn("containerlab not found in PATH; deploys will fail until it is installed")
	}

	if err := os.MkdirAll(dataDir, 0o755); err != nil {
		log.Error("create data dir", "dir", dataDir, "error", err)
		os.Exit(1)
	}

	ln, err := net.Listen("tcp", listen)
	if err != nil {
		log.Error("listen", "addr", listen, "error", err)
		os.Exit(1)
	}

	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer stop()

	if repoUpstream != "" {
		proxy := server.NewRepoProxy(repoListen, repoUpstream, log)
		if err := proxy.Start(ctx); err != nil {
			log.Error("start repo proxy", "error", err)
			os.Exit(1)
		}

		defer proxy.Stop()
	}

	srv := grpc.NewServer()
	pb.RegisterAgentServer(srv, server.New(driver, dataDir, log))

	go func() {
		<-ctx.Done()
		srv.GracefulStop()
	}()

	log.Info("agent starting", "listen", listen, "data_dir", dataDir)
	if err := srv.Serve(ln); err != nil {
		log.Error("agent exited", "error", err)
		os.Exit(1)
	}
}
