package trafficgen

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"log/slog"
	"net"
	"sync"
	"time"
)

// udpBufSize covers the largest datagram a worker can send: the seq
// prefix plus the maximum payload.
const udpBufSize = maxUDPPayloadBytes + 4096

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
	buf := make([]byte, udpBufSize)
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

// RunUDPClient runs concurrency workers, each sending one datagram
// per interval and waiting for the echo; a missing reply within the
// timeout counts as failure. A non-zero payload appends filler bytes
// to each datagram.
func RunUDPClient(ctx context.Context, log *slog.Logger, args []string) error {
	fs := flag.NewFlagSet("udp-client", flag.ContinueOnError)
	target := fs.String("target", "", "target address, e.g. 10.100.0.11:9000")
	interval := fs.Duration("interval", time.Second, "send interval per worker")
	timeout := fs.Duration("timeout", 2*time.Second, "reply timeout")
	concurrency := fs.Int("concurrency", 1, "number of parallel workers")
	payloadBytes := fs.Int("payload-bytes", 0, "extra filler bytes appended to each datagram")
	if err := fs.Parse(args); err != nil {
		return err
	}

	if *target == "" {
		return errors.New("udp-client: --target is required")
	}

	if *concurrency < 1 {
		return errors.New("udp-client: --concurrency must be >= 1")
	}

	payload, err := makePayload(*payloadBytes, maxUDPPayloadBytes)
	if err != nil {
		return fmt.Errorf("udp-client: %w", err)
	}

	st := newStats()
	go st.report(ctx, log)

	var wg sync.WaitGroup
	for range *concurrency {
		wg.Add(1)
		go func() {
			defer wg.Done()
			runUDPWorker(ctx, log, *target, *interval, *timeout, payload, st)
		}()
	}

	wg.Wait()

	return nil
}

// runUDPWorker owns one UDP socket to target until ctx is cancelled.
func runUDPWorker(ctx context.Context, log *slog.Logger, target string, interval, timeout time.Duration, payload []byte, st *stats) {
	conn, err := net.Dial("udp", target)
	if err != nil {
		log.Warn("dial failed", "error", err)

		return
	}

	defer func() { _ = conn.Close() }()

	ticker := time.NewTicker(interval)
	defer ticker.Stop()

	buf := make([]byte, udpBufSize)
	seq := 0
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			seq++
			start := time.Now()

			if err := conn.SetDeadline(time.Now().Add(timeout)); err != nil {
				log.Warn("set deadline failed", "error", err)

				continue
			}

			msg := fmt.Appendf(nil, "seq %d ", seq)
			msg = append(msg, payload...)

			if _, err := conn.Write(msg); err != nil {
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
