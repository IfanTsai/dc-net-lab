package agentdriver

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net"
	"time"

	pb "github.com/ifantsai/dcnetlab/pb/agent/v1"
)

// DialNode opens a TCP connection to addr on the lab management
// network, relayed through the agent. It backs the gRPC dialer
// towards in-container node-agents: their management bridge only
// exists on the data plane machine, so the controller cannot route to
// it directly on a split install.
func (d *Driver) DialNode(ctx context.Context, addr string) (net.Conn, error) {
	// The stream must outlive the dialing context: gRPC cancels the
	// dialer's ctx once the connection is established, but the proxied
	// connection keeps carrying traffic.
	streamCtx, cancel := context.WithCancel(context.WithoutCancel(ctx))
	stream, err := d.client.Dial(streamCtx)
	if err != nil {
		cancel()

		return nil, fromStatus(err)
	}

	if err := stream.Send(&pb.DialRequest{Address: addr}); err != nil {
		cancel()

		return nil, fromStatus(err)
	}

	return &dialConn{stream: stream, cancel: cancel, addr: addr}, nil
}

// dialConn adapts the Dial stream to net.Conn.
type dialConn struct {
	stream pb.Agent_DialClient
	cancel context.CancelFunc
	addr   string
	buf    []byte
}

func (c *dialConn) Read(p []byte) (int, error) {
	for len(c.buf) == 0 {
		msg, err := c.stream.Recv()
		if err != nil {
			if errors.Is(err, io.EOF) {
				return 0, io.EOF
			}

			return 0, fromStatus(err)
		}

		c.buf = msg.Data
	}

	n := copy(p, c.buf)
	c.buf = c.buf[n:]

	return n, nil
}

func (c *dialConn) Write(p []byte) (int, error) {
	if err := c.stream.Send(&pb.DialRequest{Data: p}); err != nil {
		return 0, fromStatus(err)
	}

	return len(p), nil
}

func (c *dialConn) Close() error {
	err := c.stream.CloseSend()
	c.cancel()

	return fromStatus(err)
}

// proxyAddr labels the connection endpoints for logging; the real
// network path runs through the agent.
type proxyAddr string

func (a proxyAddr) Network() string { return "agent" }
func (a proxyAddr) String() string  { return string(a) }

func (c *dialConn) LocalAddr() net.Addr  { return proxyAddr("controller") }
func (c *dialConn) RemoteAddr() net.Addr { return proxyAddr(c.addr) }

// Deadlines are not supported on the proxied stream; gRPC clients on
// top of it use contexts for timeouts, which cancel the stream
// itself, so nothing here depends on them.
func (c *dialConn) SetDeadline(time.Time) error      { return fmt.Errorf("deadlines not supported") }
func (c *dialConn) SetReadDeadline(time.Time) error  { return fmt.Errorf("deadlines not supported") }
func (c *dialConn) SetWriteDeadline(time.Time) error { return fmt.Errorf("deadlines not supported") }
