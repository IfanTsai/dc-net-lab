package trafficgen

import (
	"context"
	"fmt"
	"io"
	"log/slog"
	"net"
	"net/http"
	"testing"
	"time"
)

func testLogger() *slog.Logger {
	return slog.New(slog.NewTextHandler(io.Discard, nil))
}

// freePort reserves an ephemeral port and returns host:port.
func freePort(t *testing.T) string {
	t.Helper()

	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}

	defer func() { _ = ln.Close() }()

	return ln.Addr().String()
}

// waitReachable polls until a TCP dial to addr succeeds.
func waitReachable(t *testing.T, addr string) {
	t.Helper()

	for range 50 {
		conn, err := net.DialTimeout("tcp", addr, 100*time.Millisecond)
		if err == nil {
			_ = conn.Close()

			return
		}

		time.Sleep(20 * time.Millisecond)
	}

	t.Fatalf("%s not reachable", addr)
}

func TestHTTPServerEcho(t *testing.T) {
	addr := freePort(t)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	done := make(chan error, 1)
	go func() { done <- RunHTTPServer(ctx, testLogger(), []string{"--listen", addr}) }()

	waitReachable(t, addr)

	resp, err := http.Get("http://" + addr + "/ping")
	if err != nil {
		t.Fatal(err)
	}

	defer func() { _ = resp.Body.Close() }()

	body, _ := io.ReadAll(resp.Body)
	if resp.StatusCode != http.StatusOK || len(body) == 0 {
		t.Errorf("status=%d body=%q", resp.StatusCode, body)
	}

	cancel()
	if err := <-done; err != nil {
		t.Errorf("server returned %v", err)
	}
}

func TestTCPServerEcho(t *testing.T) {
	addr := freePort(t)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	done := make(chan error, 1)
	go func() { done <- RunTCPServer(ctx, testLogger(), []string{"--listen", addr}) }()

	waitReachable(t, addr)

	conn, err := net.Dial("tcp", addr)
	if err != nil {
		t.Fatal(err)
	}

	defer func() { _ = conn.Close() }()

	if _, err := fmt.Fprintln(conn, "hello"); err != nil {
		t.Fatal(err)
	}

	buf := make([]byte, 16)
	_ = conn.SetReadDeadline(time.Now().Add(time.Second))

	n, err := conn.Read(buf)
	if err != nil || string(buf[:n]) != "hello\n" {
		t.Errorf("echo: %q err=%v", buf[:n], err)
	}

	cancel()
	if err := <-done; err != nil {
		t.Errorf("server returned %v", err)
	}
}

func TestUDPServerEcho(t *testing.T) {
	pc, err := net.ListenPacket("udp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}

	addr := pc.LocalAddr().String()
	_ = pc.Close()

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	done := make(chan error, 1)
	go func() { done <- RunUDPServer(ctx, testLogger(), []string{"--listen", addr}) }()

	// UDP has no accept to probe; retry the echo until the server is up.
	conn, err := net.Dial("udp", addr)
	if err != nil {
		t.Fatal(err)
	}

	defer func() { _ = conn.Close() }()

	buf := make([]byte, 16)
	echoed := false
	for range 50 {
		_, _ = fmt.Fprint(conn, "ping")
		_ = conn.SetReadDeadline(time.Now().Add(100 * time.Millisecond))
		if n, err := conn.Read(buf); err == nil && string(buf[:n]) == "ping" {
			echoed = true

			break
		}
	}

	if !echoed {
		t.Error("no udp echo received")
	}

	cancel()
	if err := <-done; err != nil {
		t.Errorf("server returned %v", err)
	}
}

func TestClientsRequireTarget(t *testing.T) {
	ctx := context.Background()
	for _, mode := range []string{"http-client", "tcp-client", "udp-client"} {
		t.Run(mode, func(t *testing.T) {
			if err := Modes[mode](ctx, testLogger(), nil); err == nil {
				t.Error("expected error without --target")
			}
		})
	}
}

func TestRunUnknownMode(t *testing.T) {
	if err := Run(context.Background(), testLogger(), "nope", nil); err == nil {
		t.Error("expected error for unknown mode")
	}
}
