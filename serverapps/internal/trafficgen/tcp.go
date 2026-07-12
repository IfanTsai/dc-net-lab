package trafficgen

import (
	"bufio"
	"context"
	"errors"
	"flag"
	"fmt"
	"log/slog"
	"net"
	"sync"
	"time"
)

// RunTCPServer echoes lines back to every connection.
func RunTCPServer(ctx context.Context, log *slog.Logger, args []string) error {
	fs := flag.NewFlagSet("tcp-server", flag.ContinueOnError)
	listen := fs.String("listen", ":9000", "listen address")
	if err := fs.Parse(args); err != nil {
		return err
	}

	ln, err := net.Listen("tcp", *listen)
	if err != nil {
		return fmt.Errorf("tcp listen: %w", err)
	}

	st := newStats()
	go st.report(ctx, log)

	// Track live connections so shutdown terminates their handlers.
	var conns sync.Map
	go func() {
		<-ctx.Done()
		_ = ln.Close()
		conns.Range(func(key, _ any) bool {
			_ = key.(net.Conn).Close()

			return true
		})
	}()

	log.Info("listening", "addr", *listen)
	for {
		conn, err := ln.Accept()
		if err != nil {
			if ctx.Err() != nil {
				return nil
			}

			return fmt.Errorf("tcp accept: %w", err)
		}

		conns.Store(conn, struct{}{})
		go func(c net.Conn) {
			defer conns.Delete(c)
			defer func() { _ = c.Close() }()

			scanner := bufio.NewScanner(c)
			for scanner.Scan() {
				st.hit()
				if _, err := fmt.Fprintln(c, scanner.Text()); err != nil {
					return
				}
			}
		}(conn)
	}
}

// RunTCPClient sends one line per interval and expects it echoed
// back; the connection is re-dialled after failures.
func RunTCPClient(ctx context.Context, log *slog.Logger, args []string) error {
	fs := flag.NewFlagSet("tcp-client", flag.ContinueOnError)
	target := fs.String("target", "", "target address, e.g. 10.100.0.11:9000")
	interval := fs.Duration("interval", time.Second, "send interval")
	timeout := fs.Duration("timeout", 2*time.Second, "dial/echo timeout")
	if err := fs.Parse(args); err != nil {
		return err
	}

	if *target == "" {
		return errors.New("tcp-client: --target is required")
	}

	st := newStats()
	go st.report(ctx, log)

	var (
		conn   net.Conn
		reader *bufio.Reader
		seq    int
	)
	defer func() {
		if conn != nil {
			_ = conn.Close()
		}
	}()

	ticker := time.NewTicker(*interval)
	defer ticker.Stop()

	for {
		select {
		case <-ctx.Done():
			return nil
		case <-ticker.C:
			if conn == nil {
				c, err := net.DialTimeout("tcp", *target, *timeout)
				if err != nil {
					st.failure()
					log.Warn("dial failed", "error", err)

					continue
				}

				conn, reader = c, bufio.NewReader(c)
			}

			seq++
			start := time.Now()
			if err := echoOnce(conn, reader, *timeout, seq); err != nil {
				st.failure()
				log.Warn("echo failed", "seq", seq, "error", err)
				_ = conn.Close()
				conn, reader = nil, nil

				continue
			}

			st.success(time.Since(start))
		}
	}
}

// echoOnce writes one sequence line and reads the echo back.
func echoOnce(conn net.Conn, reader *bufio.Reader, timeout time.Duration, seq int) error {
	if err := conn.SetDeadline(time.Now().Add(timeout)); err != nil {
		return err
	}

	if _, err := fmt.Fprintf(conn, "seq %d\n", seq); err != nil {
		return err
	}

	if _, err := reader.ReadString('\n'); err != nil {
		return err
	}

	return nil
}
