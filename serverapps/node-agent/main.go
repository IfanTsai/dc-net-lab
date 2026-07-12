// dcnetlab-node-agent is the process supervisor inside every lab
// server container: it installs packages from the controller's
// repository, supervises Programs running out of them and serves the
// ServerAgent gRPC API on the management network for the controller.
package main

import (
	"flag"
	"fmt"
	"log/slog"
	"net"
	"os"
	"os/signal"
	"syscall"

	"google.golang.org/grpc"

	"github.com/ifantsai/dcnetlab/internal/agentapi"
	pb "github.com/ifantsai/dcnetlab/pb/nodeagent/v1"
	"github.com/ifantsai/dcnetlab/serverapps/internal/agent"
)

func main() {
	listen := flag.String("listen", fmt.Sprintf(":%d", agentapi.DefaultPort), "gRPC listen address")
	dir := flag.String("dir", "/opt/dcnetlab/run", "state directory (packages, program metas and logs)")
	flag.Parse()

	log := slog.New(slog.NewJSONHandler(os.Stdout, nil))
	if err := run(*listen, *dir, log); err != nil {
		log.Error("node-agent failed", "error", err)
		os.Exit(1)
	}
}

func run(listen, dir string, log *slog.Logger) error {
	mgr, err := agent.NewManager(dir, log)
	if err != nil {
		return fmt.Errorf("init manager: %w", err)
	}

	ln, err := net.Listen("tcp", listen)
	if err != nil {
		return fmt.Errorf("listen: %w", err)
	}

	srv := grpc.NewServer()
	pb.RegisterNodeAgentServer(srv, agent.NewService(mgr))

	sigCh := make(chan os.Signal, 1)
	signal.Notify(sigCh, syscall.SIGINT, syscall.SIGTERM)
	go func() {
		<-sigCh
		srv.GracefulStop()
	}()

	log.Info("node-agent listening", "addr", listen, "dir", dir)
	if err := srv.Serve(ln); err != nil {
		return fmt.Errorf("serve: %w", err)
	}

	mgr.Shutdown()

	return nil
}
