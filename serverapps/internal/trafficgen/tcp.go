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

// tcpScanBufMax bounds the server's per-connection line scanner so
// echoed lines carrying --payload-bytes filler fit regardless of
// what any connecting client requested.
const tcpScanBufMax = maxPayloadBytes + 4096

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
			scanner.Buffer(make([]byte, 4096), tcpScanBufMax)
			for scanner.Scan() {
				st.hit()
				if _, err := fmt.Fprintln(c, scanner.Text()); err != nil {
					return
				}
			}
		}(conn)
	}
}

// RunTCPClient runs concurrency workers, each dialling its own
// connection and sending one line per interval, expecting it echoed
// back; a connection is re-dialled after failures. A non-zero
// payload appends filler bytes to each line.
func RunTCPClient(ctx context.Context, log *slog.Logger, args []string) error {
	fs := flag.NewFlagSet("tcp-client", flag.ContinueOnError)
	target := fs.String("target", "", "target address, e.g. 10.100.0.11:9000")
	interval := fs.Duration("interval", time.Second, "send interval per worker")
	timeout := fs.Duration("timeout", 2*time.Second, "dial/echo timeout")
	concurrency := fs.Int("concurrency", 1, "number of parallel workers")
	payloadBytes := fs.Int("payload-bytes", 0, "extra filler bytes appended to each line")
	if err := fs.Parse(args); err != nil {
		return err
	}

	if *target == "" {
		return errors.New("tcp-client: --target is required")
	}

	if *concurrency < 1 {
		return errors.New("tcp-client: --concurrency must be >= 1")
	}

	payload, err := makePayload(*payloadBytes, maxPayloadBytes)
	if err != nil {
		return fmt.Errorf("tcp-client: %w", err)
	}

	st := newStats()
	go st.report(ctx, log)

	var wg sync.WaitGroup
	for range *concurrency {
		wg.Add(1)
		go func() {
			defer wg.Done()
			runTCPWorker(ctx, log, *target, *interval, *timeout, payload, st)
		}()
	}
	wg.Wait()

	return nil
}

// runTCPWorker owns one connection to target, re-dialling after
// failures, until ctx is cancelled.
func runTCPWorker(ctx context.Context, log *slog.Logger, target string, interval, timeout time.Duration, payload []byte, st *stats) {
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

	ticker := time.NewTicker(interval)
	defer ticker.Stop()

	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			if conn == nil {
				c, err := net.DialTimeout("tcp", target, timeout)
				if err != nil {
					st.failure()
					log.Warn("dial failed", "error", err)

					continue
				}

				conn, reader = c, bufio.NewReader(c)
			}

			seq++
			start := time.Now()
			if err := echoOnce(conn, reader, timeout, seq, payload); err != nil {
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

// echoOnce writes one sequence line (with optional filler payload)
// and reads the echo back.
func echoOnce(conn net.Conn, reader *bufio.Reader, timeout time.Duration, seq int, payload []byte) error {
	if err := conn.SetDeadline(time.Now().Add(timeout)); err != nil {
		return err
	}

	line := fmt.Appendf(nil, "seq %d ", seq)
	line = append(line, payload...)
	line = append(line, '\n')

	if _, err := conn.Write(line); err != nil {
		return err
	}

	if _, err := reader.ReadString('\n'); err != nil {
		return err
	}

	return nil
}
