package trafficgen

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"log/slog"
	"net"
	"time"
)

// RunUDPServer echoes datagrams back to their sender.
func RunUDPServer(ctx context.Context, log *slog.Logger, args []string) error {
	fs := flag.NewFlagSet("udp-server", flag.ContinueOnError)
	listen := fs.String("listen", ":9000", "listen address")
	if err := fs.Parse(args); err != nil {
		return err
	}

	pc, err := net.ListenPacket("udp", *listen)
	if err != nil {
		return fmt.Errorf("udp listen: %w", err)
	}

	st := newStats()
	go st.report(ctx, log)
	go func() {
		<-ctx.Done()
		_ = pc.Close()
	}()

	log.Info("listening", "addr", *listen)
	buf := make([]byte, 64*1024)
	for {
		n, addr, err := pc.ReadFrom(buf)
		if err != nil {
			if ctx.Err() != nil {
				return nil
			}

			return fmt.Errorf("udp read: %w", err)
		}

		st.hit()
		if _, err := pc.WriteTo(buf[:n], addr); err != nil {
			log.Warn("udp reply failed", "peer", addr.String(), "error", err)
		}
	}
}

// RunUDPClient sends one datagram per interval and waits for the
// echo; a missing reply within the timeout counts as failure.
func RunUDPClient(ctx context.Context, log *slog.Logger, args []string) error {
	fs := flag.NewFlagSet("udp-client", flag.ContinueOnError)
	target := fs.String("target", "", "target address, e.g. 10.100.0.11:9000")
	interval := fs.Duration("interval", time.Second, "send interval")
	timeout := fs.Duration("timeout", 2*time.Second, "reply timeout")
	if err := fs.Parse(args); err != nil {
		return err
	}

	if *target == "" {
		return errors.New("udp-client: --target is required")
	}

	conn, err := net.Dial("udp", *target)
	if err != nil {
		return fmt.Errorf("udp dial: %w", err)
	}

	defer func() { _ = conn.Close() }()

	st := newStats()
	go st.report(ctx, log)

	ticker := time.NewTicker(*interval)
	defer ticker.Stop()

	buf := make([]byte, 64*1024)
	seq := 0
	for {
		select {
		case <-ctx.Done():
			return nil
		case <-ticker.C:
			seq++
			start := time.Now()

			if err := conn.SetDeadline(time.Now().Add(*timeout)); err != nil {
				return fmt.Errorf("set deadline: %w", err)
			}

			if _, err := fmt.Fprintf(conn, "seq %d", seq); err != nil {
				st.failure()
				log.Warn("send failed", "seq", seq, "error", err)

				continue
			}

			if _, err := conn.Read(buf); err != nil {
				st.failure()
				log.Warn("no reply", "seq", seq, "error", err)

				continue
			}

			st.success(time.Since(start))
		}
	}
}
