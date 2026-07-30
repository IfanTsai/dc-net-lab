package server

import (
	"context"
	"fmt"
	"io"
	"log/slog"
	"net"
)

// RepoProxy forwards TCP connections from lab containers to the
// controller's package repository. Containers reach their management
// gateway — an address on this host — so on a multi-machine install
// the agent must relay repository traffic to wherever the controller
// actually runs. On a single machine the controller's own listener
// answers directly and no proxy is needed.
type RepoProxy struct {
	listen   string
	upstream string
	log      *slog.Logger

	ln net.Listener
}

// NewRepoProxy builds a proxy from listen (the address containers
// dial, conventionally :50062) to the controller's repository at
// upstream.
func NewRepoProxy(listen, upstream string, log *slog.Logger) *RepoProxy {
	return &RepoProxy{listen: listen, upstream: upstream, log: log}
}

// Start begins accepting; it returns once the listener is bound.
func (p *RepoProxy) Start(ctx context.Context) error {
	ln, err := net.Listen("tcp", p.listen)
	if err != nil {
		return fmt.Errorf("repo proxy listen on %s: %w", p.listen, err)
	}

	p.ln = ln
	p.log.Info("repo proxy forwarding", "listen", p.listen, "upstream", p.upstream)

	go func() {
		for {
			conn, err := ln.Accept()
			if err != nil {
				return
			}

			go p.forward(ctx, conn)
		}
	}()

	return nil
}

// Stop closes the listener; in-flight connections finish on their own.
func (p *RepoProxy) Stop() {
	if p.ln != nil {
		_ = p.ln.Close()
	}
}

func (p *RepoProxy) forward(ctx context.Context, conn net.Conn) {
	defer func() { _ = conn.Close() }()

	var d net.Dialer
	up, err := d.DialContext(ctx, "tcp", p.upstream)
	if err != nil {
		p.log.Warn("repo proxy dial upstream failed", "upstream", p.upstream, "error", err)

		return
	}

	defer func() { _ = up.Close() }()

	done := make(chan struct{})
	go func() {
		_, _ = io.Copy(up, conn)
		if tc, ok := up.(*net.TCPConn); ok {
			_ = tc.CloseWrite()
		}

		close(done)
	}()

	_, _ = io.Copy(conn, up)

	<-done
}
