package trafficgen

import (
	"bufio"
	"bytes"
	"context"
	"fmt"
	"io"
	"log/slog"
	"net"
	"net/http"
	"net/http/httptest"
	"reflect"
	"strings"
	"sync/atomic"
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

func TestLatencyPercentiles(t *testing.T) {
	if _, _, _, ok := latencyPercentiles(nil); ok {
		t.Error("expected ok=false for empty samples")
	}

	samples := make([]int64, 0, 100)
	for i := int64(1); i <= 100; i++ {
		samples = append(samples, i)
	}

	p50, p95, p99, ok := latencyPercentiles(samples)
	if !ok {
		t.Fatal("expected ok=true")
	}

	if p50 != 50 || p95 != 95 || p99 != 99 {
		t.Errorf("p50=%d p95=%d p99=%d, want 50/95/99", p50, p95, p99)
	}

	// Unsorted input and a single sample.
	if p50, p95, p99, ok := latencyPercentiles([]int64{30, 10, 20}); !ok || p50 != 20 || p95 != 20 || p99 != 20 {
		t.Errorf("unsorted: p50=%d p95=%d p99=%d ok=%v", p50, p95, p99, ok)
	}

	if p50, p95, p99, ok := latencyPercentiles([]int64{42}); !ok || p50 != 42 || p95 != 42 || p99 != 42 {
		t.Errorf("single: p50=%d p95=%d p99=%d ok=%v", p50, p95, p99, ok)
	}
}

func TestMakePayload(t *testing.T) {
	if buf, err := makePayload(0, 100); err != nil || buf != nil {
		t.Errorf("n=0: buf=%v err=%v, want nil, nil", buf, err)
	}

	if _, err := makePayload(-1, 100); err == nil {
		t.Error("expected error for negative n")
	}

	if _, err := makePayload(101, 100); err == nil {
		t.Error("expected error for n > max")
	}

	buf, err := makePayload(16, 100)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if len(buf) != 16 || !reflect.DeepEqual(buf, bytes.Repeat([]byte("x"), 16)) {
		t.Errorf("buf=%q, want 16 'x' bytes", buf)
	}
}

func TestDoRequestSwitchesMethodOnPayload(t *testing.T) {
	var gotMethod string
	var gotBodyLen int

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotMethod = r.Method
		body, _ := io.ReadAll(r.Body)
		gotBodyLen = len(body)
		w.WriteHeader(http.StatusOK)
	}))
	defer srv.Close()

	client := srv.Client()

	if status, err := doRequest(context.Background(), client, srv.URL, nil); err != nil || status != http.StatusOK {
		t.Fatalf("no payload: status=%d err=%v", status, err)
	}

	if gotMethod != http.MethodGet {
		t.Errorf("no payload: method=%q, want GET", gotMethod)
	}

	payload, err := makePayload(64, maxPayloadBytes)
	if err != nil {
		t.Fatal(err)
	}

	if status, err := doRequest(context.Background(), client, srv.URL, payload); err != nil || status != http.StatusOK {
		t.Fatalf("with payload: status=%d err=%v", status, err)
	}

	if gotMethod != http.MethodPost || gotBodyLen != 64 {
		t.Errorf("with payload: method=%q bodyLen=%d, want POST/64", gotMethod, gotBodyLen)
	}
}

func TestClientsRejectBadFlags(t *testing.T) {
	cases := []struct {
		mode string
		args []string
	}{
		{"http-client", []string{"--target", "http://example.invalid", "--concurrency", "0"}},
		{"http-client", []string{"--target", "http://example.invalid", "--payload-bytes", "-1"}},
		{"tcp-client", []string{"--target", "example.invalid:9000", "--concurrency", "0"}},
		{"tcp-client", []string{"--target", "example.invalid:9000", "--payload-bytes", "-1"}},
		{"udp-client", []string{"--target", "example.invalid:9000", "--concurrency", "0"}},
		{"udp-client", []string{"--target", "example.invalid:9000", "--payload-bytes", "-1"}},
		{"udp-client", []string{"--target", "example.invalid:9000", "--payload-bytes", "70000"}},
	}

	for _, c := range cases {
		t.Run(c.mode+"_"+strings.Join(c.args, "_"), func(t *testing.T) {
			if err := Modes[c.mode](context.Background(), testLogger(), c.args); err == nil {
				t.Errorf("%s %v: expected error", c.mode, c.args)
			}
		})
	}
}

func TestTCPServerEchoesLargeLine(t *testing.T) {
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

	line := strings.Repeat("y", 200000)
	if _, err := fmt.Fprintln(conn, line); err != nil {
		t.Fatal(err)
	}

	reader := bufio.NewReaderSize(conn, 4096)
	_ = conn.SetReadDeadline(time.Now().Add(2 * time.Second))

	got, err := reader.ReadString('\n')
	if err != nil {
		t.Fatalf("read echo: %v", err)
	}

	if strings.TrimRight(got, "\n") != line {
		t.Errorf("echoed length=%d, want %d", len(got), len(line))
	}

	cancel()
	if err := <-done; err != nil {
		t.Errorf("server returned %v", err)
	}
}

func TestHTTPClientConcurrencyAndPayload(t *testing.T) {
	var hits atomic.Int64
	var maxBody atomic.Int64

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		if int64(len(body)) > maxBody.Load() {
			maxBody.Store(int64(len(body)))
		}

		hits.Add(1)
		w.WriteHeader(http.StatusOK)
	}))
	defer srv.Close()

	ctx, cancel := context.WithTimeout(context.Background(), 300*time.Millisecond)
	defer cancel()

	err := RunHTTPClient(ctx, testLogger(), []string{
		"--target", srv.URL,
		"--interval", "20ms",
		"--concurrency", "3",
		"--payload-bytes", "32",
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if hits.Load() < 3 {
		t.Errorf("hits=%d, want at least 3 (3 workers x >=1 tick)", hits.Load())
	}

	if maxBody.Load() != 32 {
		t.Errorf("maxBody=%d, want 32", maxBody.Load())
	}
}

func TestUDPClientConcurrencyAndPayload(t *testing.T) {
	addr := freePort(t)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	done := make(chan error, 1)
	go func() { done <- RunUDPServer(ctx, testLogger(), []string{"--listen", addr}) }()

	clientCtx, clientCancel := context.WithTimeout(ctx, 300*time.Millisecond)
	defer clientCancel()

	err := RunUDPClient(clientCtx, testLogger(), []string{
		"--target", addr,
		"--interval", "20ms",
		"--concurrency", "2",
		"--payload-bytes", "40",
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	cancel()
	if err := <-done; err != nil {
		t.Errorf("server returned %v", err)
	}
}
