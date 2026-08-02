// Package agentdriver implements runtime.Driver over the data plane
// gRPC API. It is how the controller reaches the data plane: on a
// single machine the agent listens on localhost, on a split install on
// a routable address — the code path is the same either way.
package agentdriver

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"

	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/credentials/insecure"
	"google.golang.org/grpc/status"

	"github.com/ifantsai/dcnetlab/internal/runtime"
	pb "github.com/ifantsai/dcnetlab/pb/agent/v1"
)

// Driver talks to one agent. dataRoot is the controller's data
// directory; generation directories under it keep their relative path
// as the artifact key on the agent side, so agent-side layout mirrors
// the controller's.
type Driver struct {
	client   pb.AgentClient
	conn     *grpc.ClientConn
	dataRoot string
}

// New dials the agent at addr. The returned cleanup closes the
// connection.
func New(addr, dataRoot string) (*Driver, func(), error) {
	conn, err := grpc.NewClient(addr, grpc.WithTransportCredentials(insecure.NewCredentials()))
	if err != nil {
		return nil, nil, fmt.Errorf("dial agent %s: %w", addr, err)
	}

	d := &Driver{client: pb.NewAgentClient(conn), conn: conn, dataRoot: dataRoot}

	return d, func() { _ = conn.Close() }, nil
}

func (d *Driver) Name() string { return "agent" }

// Ping reports whether the agent is reachable and its runtime usable;
// detail explains an unusable runtime. WaitForReady rides out the
// startup race when agent and controller are launched together — the
// caller's context bounds the wait.
func (d *Driver) Ping(ctx context.Context) (available bool, detail string, err error) {
	reply, err := d.client.Ping(ctx, &pb.PingRequest{}, grpc.WaitForReady(true))
	if err != nil {
		return false, "", fromStatus(err)
	}

	return reply.RuntimeAvailable, reply.Detail, nil
}

// fromStatus restores the driver error semantics encoded by the
// agent: Unavailable → runtime.ErrUnavailable (runtime itself down,
// callers fail fast), Unimplemented → runtime.ErrNotSupported. A
// transport-level Unavailable (agent unreachable) folds into the same
// ErrUnavailable — the runtime is equally gone either way.
func fromStatus(err error) error {
	if err == nil {
		return nil
	}

	switch status.Code(err) {
	case codes.Unavailable:
		return fmt.Errorf("%w: %s", runtime.ErrUnavailable, status.Convert(err).Message())
	case codes.Unimplemented:
		return fmt.Errorf("%w: %s", runtime.ErrNotSupported, status.Convert(err).Message())
	default:
		return fmt.Errorf("agent: %s", status.Convert(err).Message())
	}
}

// artifact reads the generation directory into a Deploy payload. The
// key is the directory's path relative to the controller's data root
// so repeated deploys of one generation land in one agent-side
// directory.
func (d *Driver) artifact(dir string) (*pb.DeployRequest, error) {
	key, err := filepath.Rel(d.dataRoot, dir)
	if err != nil || !filepath.IsLocal(key) {
		key = strings.NewReplacer("/", "_", string(filepath.Separator), "_", ":", "_").Replace(dir)
	}

	files := make(map[string][]byte)
	walkErr := filepath.WalkDir(dir, func(path string, entry os.DirEntry, err error) error {
		if err != nil || entry.IsDir() {
			return err
		}

		rel, err := filepath.Rel(dir, path)
		if err != nil {
			return err
		}

		content, err := os.ReadFile(path)
		if err != nil {
			return err
		}

		files[filepath.ToSlash(rel)] = content

		return nil
	})
	if walkErr != nil {
		return nil, fmt.Errorf("read artifact %s: %w", dir, walkErr)
	}

	return &pb.DeployRequest{Key: filepath.ToSlash(key), Files: files}, nil
}

func (d *Driver) Deploy(ctx context.Context, dir string) error {
	req, err := d.artifact(dir)
	if err != nil {
		return err
	}

	_, err = d.client.Deploy(ctx, req)

	return fromStatus(err)
}

func (d *Driver) DeployIncrement(ctx context.Context, dir string) error {
	req, err := d.artifact(dir)
	if err != nil {
		return err
	}

	req.Increment = true
	_, err = d.client.Deploy(ctx, req)

	return fromStatus(err)
}

func (d *Driver) Destroy(ctx context.Context, dir string) error {
	req, err := d.artifact(dir)
	if err != nil {
		return err
	}

	_, err = d.client.Destroy(ctx, req)

	return fromStatus(err)
}

func (d *Driver) Exec(ctx context.Context, labName, nodeName string, cmd []string) ([]byte, error) {
	reply, err := d.client.Exec(ctx, &pb.ExecRequest{LabName: labName, NodeName: nodeName, Cmd: cmd})
	if err != nil {
		return nil, fromStatus(err)
	}

	return reply.Output, nil
}

// ExecStream opens the streaming exec. Closing the returned session
// half-closes the client side, which the agent translates into the
// in-container tool's stdin EOF — the same stop-signal contract as
// the local driver.
func (d *Driver) ExecStream(ctx context.Context, labName, nodeName string, cmd []string) (runtime.ExecSession, error) {
	ctx, cancel := context.WithCancel(ctx)
	stream, err := d.client.ExecStream(ctx)
	if err != nil {
		cancel()

		return nil, fromStatus(err)
	}

	start := &pb.ExecStreamRequest{Start: &pb.ExecRequest{LabName: labName, NodeName: nodeName, Cmd: cmd}}
	if err := stream.Send(start); err != nil {
		cancel()

		return nil, fromStatus(err)
	}

	return &execSession{stream: stream, cancel: cancel}, nil
}

// execSession adapts the gRPC stream to runtime.ExecSession.
type execSession struct {
	stream pb.Agent_ExecStreamClient
	cancel context.CancelFunc
	buf    []byte
	err    error
}

func (s *execSession) Read(p []byte) (int, error) {
	for len(s.buf) == 0 {
		if s.err != nil {
			return 0, s.err
		}

		msg, err := s.stream.Recv()
		if err != nil {
			if errors.Is(err, io.EOF) {
				s.err = io.EOF
			} else {
				s.err = fromStatus(err)
			}

			return 0, s.err
		}

		s.buf = msg.Stdout
	}

	n := copy(p, s.buf)
	s.buf = s.buf[n:]

	return n, nil
}

// Close half-closes the stream — the agent closes the tool's stdin —
// then drains until the agent reports the exit outcome.
func (s *execSession) Close() error {
	defer s.cancel()

	if err := s.stream.CloseSend(); err != nil {
		return fromStatus(err)
	}

	for {
		msg, err := s.stream.Recv()
		if errors.Is(err, io.EOF) {
			return nil
		}

		if err != nil {
			return fromStatus(err)
		}

		// Late stdout after Close is dropped, matching the local
		// driver where the reader stops consuming before reaping.
		_ = msg
	}
}

// OpenTerminal opens the interactive terminal stream.
func (d *Driver) OpenTerminal(ctx context.Context, labName, nodeName string, cmd []string) (runtime.TerminalSession, error) {
	ctx, cancel := context.WithCancel(ctx)
	stream, err := d.client.Terminal(ctx)
	if err != nil {
		cancel()

		return nil, fromStatus(err)
	}

	start := &pb.TerminalRequest{Start: &pb.TerminalStart{LabName: labName, NodeName: nodeName, Cmd: cmd}}
	if err := stream.Send(start); err != nil {
		cancel()

		return nil, fromStatus(err)
	}

	return &terminalSession{stream: stream, cancel: cancel}, nil
}

// terminalSession adapts the gRPC stream to runtime.TerminalSession.
type terminalSession struct {
	stream pb.Agent_TerminalClient
	cancel context.CancelFunc
	buf    []byte
}

func (s *terminalSession) Read(p []byte) (int, error) {
	for len(s.buf) == 0 {
		msg, err := s.stream.Recv()
		if err != nil {
			if errors.Is(err, io.EOF) {
				return 0, io.EOF
			}

			return 0, fromStatus(err)
		}

		s.buf = msg.Stdout
	}

	n := copy(p, s.buf)
	s.buf = s.buf[n:]

	return n, nil
}

func (s *terminalSession) Write(p []byte) (int, error) {
	if err := s.stream.Send(&pb.TerminalRequest{Stdin: p}); err != nil {
		return 0, fromStatus(err)
	}

	return len(p), nil
}

func (s *terminalSession) Resize(cols, rows uint16) error {
	req := &pb.TerminalRequest{Resize: &pb.TerminalResize{Cols: uint32(cols), Rows: uint32(rows)}}

	return fromStatus(s.stream.Send(req))
}

func (s *terminalSession) Close() error {
	defer s.cancel()

	return fromStatus(s.stream.CloseSend())
}

func (d *Driver) NodeStates(ctx context.Context, labName string, nodeNames []string) (map[string]string, error) {
	reply, err := d.client.NodeStates(ctx, &pb.NodeStatesRequest{LabName: labName, NodeNames: nodeNames})
	if err != nil {
		return nil, fromStatus(err)
	}

	return reply.States, nil
}

func (d *Driver) NodeAddress(ctx context.Context, labName, nodeName string) (string, error) {
	reply, err := d.client.NodeAddress(ctx, &pb.NodeRequest{LabName: labName, NodeName: nodeName})
	if err != nil {
		return "", fromStatus(err)
	}

	return reply.Address, nil
}

func (d *Driver) NodeGateway(ctx context.Context, labName, nodeName string) (string, error) {
	reply, err := d.client.NodeGateway(ctx, &pb.NodeRequest{LabName: labName, NodeName: nodeName})
	if err != nil {
		return "", fromStatus(err)
	}

	return reply.Address, nil
}

func (d *Driver) StartNodes(ctx context.Context, labName string, nodeNames []string) error {
	_, err := d.client.StartNodes(ctx, &pb.NodeSetRequest{LabName: labName, NodeNames: nodeNames})

	return fromStatus(err)
}

func (d *Driver) StopNodes(ctx context.Context, labName string, nodeNames []string) error {
	_, err := d.client.StopNodes(ctx, &pb.NodeSetRequest{LabName: labName, NodeNames: nodeNames})

	return fromStatus(err)
}

func (d *Driver) EnsureImage(ctx context.Context, image string) error {
	_, err := d.client.EnsureImage(ctx, &pb.EnsureImageRequest{Image: image})

	return fromStatus(err)
}

func (d *Driver) ConnectInternet(ctx context.Context, labName, nodeName string) error {
	_, err := d.client.ConnectInternet(ctx, &pb.NodeRequest{LabName: labName, NodeName: nodeName})

	return fromStatus(err)
}

func (d *Driver) SetInterfaceState(ctx context.Context, labName, nodeName, iface string, up bool) error {
	req := &pb.SetInterfaceStateRequest{LabName: labName, NodeName: nodeName, Interface: iface, Up: up}
	_, err := d.client.SetInterfaceState(ctx, req)

	return fromStatus(err)
}

func (d *Driver) ApplyImpairment(ctx context.Context, labName, nodeName, iface string, imp runtime.Impairment) error {
	req := &pb.ApplyImpairmentRequest{
		LabName:     labName,
		NodeName:    nodeName,
		Interface:   iface,
		DelayMs:     int32(imp.DelayMs),
		JitterMs:    int32(imp.JitterMs),
		LossPercent: imp.LossPercent,
		RateKbit:    int32(imp.RateKbit),
	}

	_, err := d.client.ApplyImpairment(ctx, req)

	return fromStatus(err)
}

func (d *Driver) ClearImpairment(ctx context.Context, labName, nodeName, iface string) error {
	req := &pb.ClearImpairmentRequest{LabName: labName, NodeName: nodeName, Interface: iface}
	_, err := d.client.ClearImpairment(ctx, req)

	return fromStatus(err)
}
