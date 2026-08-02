// Package server exposes the containerlab runtime driver over
// gRPC. It is the data plane's front door: the controller sends
// deploy artifacts and driver operations here instead of shelling out
// to docker/containerlab itself, so the two planes no longer need to
// share a machine.
package server

import (
	"context"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"sync"

	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"

	"github.com/ifantsai/dcnetlab/agent/internal/clab"
	"github.com/ifantsai/dcnetlab/internal/runtime"
	pb "github.com/ifantsai/dcnetlab/pb/agent/v1"
)

// copyBufSize is the chunk size for exec/terminal/dial streaming.
const copyBufSize = 32 * 1024

// Server implements the Agent gRPC service over the containerlab
// driver.
type Server struct {
	pb.UnimplementedAgentServer

	driver  *clab.ContainerlabDriver
	dataDir string
	log     *slog.Logger
}

// New builds the server; dataDir is where deploy artifacts are
// materialised.
func New(driver *clab.ContainerlabDriver, dataDir string, log *slog.Logger) *Server {
	return &Server{driver: driver, dataDir: dataDir, log: log}
}

// toStatus maps driver errors onto gRPC status codes so the
// controller-side client can restore runtime.ErrUnavailable /
// runtime.ErrNotSupported semantics.
func toStatus(err error) error {
	switch {
	case err == nil:
		return nil
	case errors.Is(err, runtime.ErrUnavailable):
		return status.Error(codes.Unavailable, err.Error())
	case errors.Is(err, runtime.ErrNotSupported):
		return status.Error(codes.Unimplemented, err.Error())
	default:
		return status.Error(codes.Unknown, err.Error())
	}
}

// Ping reports whether the containerlab runtime behind the agent is
// usable.
func (s *Server) Ping(ctx context.Context, _ *pb.PingRequest) (*pb.PingReply, error) {
	if !s.driver.Available() {
		return &pb.PingReply{Detail: "containerlab binary not found in PATH"}, nil
	}

	if out, err := exec.CommandContext(ctx, "docker", "version", "--format", "{{.Server.Version}}").CombinedOutput(); err != nil {
		return &pb.PingReply{Detail: fmt.Sprintf("docker daemon unavailable: %s", out)}, nil
	}

	return &pb.PingReply{RuntimeAvailable: true}, nil
}

// materialise writes a generation artifact under the agent's data
// directory and returns the directory. The key is validated to stay
// inside dataDir, as is every file path inside the artifact.
func (s *Server) materialise(key string, files map[string][]byte) (string, error) {
	if key == "" || !filepath.IsLocal(key) {
		return "", status.Errorf(codes.InvalidArgument, "artifact key %q escapes the data directory", key)
	}

	dir := filepath.Join(s.dataDir, "deployments", key)
	for rel, content := range files {
		if !filepath.IsLocal(rel) {
			return "", status.Errorf(codes.InvalidArgument, "artifact path %q escapes the generation directory", rel)
		}

		path := filepath.Join(dir, rel)
		if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
			return "", status.Error(codes.Internal, err.Error())
		}

		if err := os.WriteFile(path, content, 0o644); err != nil {
			return "", status.Error(codes.Internal, err.Error())
		}
	}

	return dir, nil
}

func (s *Server) Deploy(ctx context.Context, req *pb.DeployRequest) (*pb.DeployReply, error) {
	dir, err := s.materialise(req.Key, req.Files)
	if err != nil {
		return nil, err
	}

	if req.Increment {
		s.log.Info("deploying generation increment", "key", req.Key)
		if err := s.driver.DeployIncrement(ctx, dir); err != nil {
			return nil, toStatus(err)
		}

		return &pb.DeployReply{}, nil
	}

	s.log.Info("deploying generation", "key", req.Key)
	if err := s.driver.Deploy(ctx, dir); err != nil {
		return nil, toStatus(err)
	}

	return &pb.DeployReply{}, nil
}

func (s *Server) Destroy(ctx context.Context, req *pb.DeployRequest) (*pb.DeployReply, error) {
	dir, err := s.materialise(req.Key, req.Files)
	if err != nil {
		return nil, err
	}

	s.log.Info("destroying generation", "key", req.Key)
	if err := s.driver.Destroy(ctx, dir); err != nil {
		return nil, toStatus(err)
	}

	return &pb.DeployReply{}, nil
}

func (s *Server) Exec(ctx context.Context, req *pb.ExecRequest) (*pb.ExecReply, error) {
	out, err := s.driver.Exec(ctx, req.LabName, req.NodeName, req.Cmd)
	if err != nil {
		// The combined output is already folded into the error text by
		// the driver, so the client loses nothing when the reply is
		// dropped on error.
		return nil, toStatus(err)
	}

	return &pb.ExecReply{Output: out}, nil
}

// ExecStream bridges the driver's exec session onto a bidirectional
// stream. The client's half-close (CloseSend) closes the in-container
// command's stdin — its stop signal — mirroring the local ExecSession
// contract.
func (s *Server) ExecStream(stream pb.Agent_ExecStreamServer) error {
	first, err := stream.Recv()
	if err != nil {
		return err
	}

	if first.Start == nil {
		return status.Error(codes.InvalidArgument, "first exec stream message must carry start")
	}

	sess, err := s.driver.ExecStream(stream.Context(), first.Start.LabName, first.Start.NodeName, first.Start.Cmd)
	if err != nil {
		return toStatus(err)
	}

	closeOnce := onceClose(sess)

	// The client signals stop by half-closing its side; a dropped
	// connection surfaces as a Recv error and stops the tool the same
	// way, so no orphan survives a dead controller.
	go func() {
		for {
			if _, err := stream.Recv(); err != nil {
				_ = closeOnce()

				return
			}
		}
	}()

	buf := make([]byte, copyBufSize)
	for {
		n, readErr := sess.Read(buf)
		if n > 0 {
			if err := stream.Send(&pb.ExecStreamReply{Stdout: buf[:n]}); err != nil {
				_ = closeOnce()

				return err
			}
		}

		if readErr != nil {
			// Close reaps the process and reports stderr on abnormal
			// exit; that error is the stream's outcome.
			return toStatus(closeOnce())
		}
	}
}

// onceClose wraps an io.Closer so concurrent close paths (client
// half-close vs. tool exit) reap the process exactly once and share
// the exit error.
func onceClose(c io.Closer) func() error {
	var (
		once sync.Once
		err  error
	)

	return func() error {
		once.Do(func() { err = c.Close() })

		return err
	}
}

// Terminal bridges an interactive pty session onto a bidirectional
// stream: stdin bytes and resize events flow in, terminal output
// flows out.
func (s *Server) Terminal(stream pb.Agent_TerminalServer) error {
	first, err := stream.Recv()
	if err != nil {
		return err
	}

	if first.Start == nil {
		return status.Error(codes.InvalidArgument, "first terminal message must carry start")
	}

	sess, err := s.driver.OpenTerminal(stream.Context(), first.Start.LabName, first.Start.NodeName, first.Start.Cmd)
	if err != nil {
		return toStatus(err)
	}

	defer func() { _ = sess.Close() }()

	go func() {
		for {
			msg, err := stream.Recv()
			if err != nil {
				_ = sess.Close()

				return
			}

			if len(msg.Stdin) > 0 {
				if _, err := sess.Write(msg.Stdin); err != nil {
					return
				}
			}

			if msg.Resize != nil {
				_ = sess.Resize(uint16(msg.Resize.Cols), uint16(msg.Resize.Rows))
			}
		}
	}()

	buf := make([]byte, copyBufSize)
	for {
		n, readErr := sess.Read(buf)
		if n > 0 {
			if err := stream.Send(&pb.TerminalReply{Stdout: buf[:n]}); err != nil {
				return err
			}
		}

		if readErr != nil {
			// The pty master returns an error when the shell exits;
			// that is the normal end of a terminal session.
			return nil
		}
	}
}

// Dial proxies one TCP connection into the lab management network so
// the controller can reach in-container agents from another machine.
func (s *Server) Dial(stream pb.Agent_DialServer) error {
	first, err := stream.Recv()
	if err != nil {
		return err
	}

	if first.Address == "" {
		return status.Error(codes.InvalidArgument, "first dial message must carry the target address")
	}

	var d net.Dialer
	conn, err := d.DialContext(stream.Context(), "tcp", first.Address)
	if err != nil {
		return status.Errorf(codes.Unavailable, "dial %s: %v", first.Address, err)
	}

	defer func() { _ = conn.Close() }()

	if len(first.Data) > 0 {
		if _, err := conn.Write(first.Data); err != nil {
			return status.Error(codes.Unavailable, err.Error())
		}
	}

	go func() {
		for {
			msg, err := stream.Recv()
			if err != nil {
				// Client half-close or drop: no more bytes towards the
				// target; shut down the write side so the target sees
				// EOF while responses keep flowing.
				if tc, ok := conn.(*net.TCPConn); ok {
					_ = tc.CloseWrite()
				}

				return
			}

			if len(msg.Data) > 0 {
				if _, err := conn.Write(msg.Data); err != nil {
					return
				}
			}
		}
	}()

	buf := make([]byte, copyBufSize)
	for {
		n, readErr := conn.Read(buf)
		if n > 0 {
			if err := stream.Send(&pb.DialReply{Data: buf[:n]}); err != nil {
				return err
			}
		}

		if readErr != nil {
			return nil
		}
	}
}

func (s *Server) NodeStates(ctx context.Context, req *pb.NodeStatesRequest) (*pb.NodeStatesReply, error) {
	states, err := s.driver.NodeStates(ctx, req.LabName, req.NodeNames)
	if err != nil {
		return nil, toStatus(err)
	}

	return &pb.NodeStatesReply{States: states}, nil
}

func (s *Server) NodeAddress(ctx context.Context, req *pb.NodeRequest) (*pb.AddressReply, error) {
	addr, err := s.driver.NodeAddress(ctx, req.LabName, req.NodeName)
	if err != nil {
		return nil, toStatus(err)
	}

	return &pb.AddressReply{Address: addr}, nil
}

func (s *Server) NodeGateway(ctx context.Context, req *pb.NodeRequest) (*pb.AddressReply, error) {
	addr, err := s.driver.NodeGateway(ctx, req.LabName, req.NodeName)
	if err != nil {
		return nil, toStatus(err)
	}

	return &pb.AddressReply{Address: addr}, nil
}

func (s *Server) StartNodes(ctx context.Context, req *pb.NodeSetRequest) (*pb.NodeSetReply, error) {
	if err := s.driver.StartNodes(ctx, req.LabName, req.NodeNames); err != nil {
		return nil, toStatus(err)
	}

	return &pb.NodeSetReply{}, nil
}

func (s *Server) StopNodes(ctx context.Context, req *pb.NodeSetRequest) (*pb.NodeSetReply, error) {
	if err := s.driver.StopNodes(ctx, req.LabName, req.NodeNames); err != nil {
		return nil, toStatus(err)
	}

	return &pb.NodeSetReply{}, nil
}

func (s *Server) EnsureImage(ctx context.Context, req *pb.EnsureImageRequest) (*pb.EnsureImageReply, error) {
	if err := s.driver.EnsureImage(ctx, req.Image); err != nil {
		return nil, toStatus(err)
	}

	return &pb.EnsureImageReply{}, nil
}

func (s *Server) ConnectInternet(ctx context.Context, req *pb.NodeRequest) (*pb.ConnectInternetReply, error) {
	if err := s.driver.ConnectInternet(ctx, req.LabName, req.NodeName); err != nil {
		return nil, toStatus(err)
	}

	return &pb.ConnectInternetReply{}, nil
}

func (s *Server) SetInterfaceState(ctx context.Context, req *pb.SetInterfaceStateRequest) (*pb.SetInterfaceStateReply, error) {
	if err := s.driver.SetInterfaceState(ctx, req.LabName, req.NodeName, req.Interface, req.Up); err != nil {
		return nil, toStatus(err)
	}

	return &pb.SetInterfaceStateReply{}, nil
}

func (s *Server) ApplyImpairment(ctx context.Context, req *pb.ApplyImpairmentRequest) (*pb.ImpairmentReply, error) {
	imp := runtime.Impairment{
		DelayMs:     int(req.DelayMs),
		JitterMs:    int(req.JitterMs),
		LossPercent: req.LossPercent,
		RateKbit:    int(req.RateKbit),
	}

	if err := s.driver.ApplyImpairment(ctx, req.LabName, req.NodeName, req.Interface, imp); err != nil {
		return nil, toStatus(err)
	}

	return &pb.ImpairmentReply{}, nil
}

func (s *Server) ClearImpairment(ctx context.Context, req *pb.ClearImpairmentRequest) (*pb.ImpairmentReply, error) {
	if err := s.driver.ClearImpairment(ctx, req.LabName, req.NodeName, req.Interface); err != nil {
		return nil, toStatus(err)
	}

	return &pb.ImpairmentReply{}, nil
}
