package trafficgen

import (
	"bytes"
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"os"
	"sync"
	"time"
)

// RunHTTPServer serves a small JSON echo endpoint and logs a stat
// line per window.
func RunHTTPServer(ctx context.Context, log *slog.Logger, args []string) error {
	fs := flag.NewFlagSet("http-server", flag.ContinueOnError)
	listen := fs.String("listen", ":8080", "listen address")
	if err := fs.Parse(args); err != nil {
		return err
	}

	st := newStats()
	hostname, _ := os.Hostname()
	mux := http.NewServeMux()
	mux.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) {
		if r.Body != nil {
			_, _ = io.Copy(io.Discard, r.Body)
		}

		st.hit()
		w.Header().Set("Content-Type", "application/json")
		_, _ = fmt.Fprintf(w, `{"host":%q,"path":%q,"time":%q}`, hostname, r.URL.Path, time.Now().UTC().Format(time.RFC3339Nano))
	})

	srv := &http.Server{Addr: *listen, Handler: mux, ReadHeaderTimeout: 5 * time.Second}
	go func() {
		<-ctx.Done()
		shutdownCtx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
		defer cancel()
		_ = srv.Shutdown(shutdownCtx)
	}()

	go st.report(ctx, log)

	log.Info("listening", "addr", *listen)
	if err := srv.ListenAndServe(); !errors.Is(err, http.ErrServerClosed) {
		return fmt.Errorf("http server: %w", err)
	}

	return nil
}

// RunHTTPClient runs concurrency workers, each issuing one request
// per interval against the target and recording success rate and
// latency; a non-zero payload switches the request to a POST with
// that many filler bytes as body.
func RunHTTPClient(ctx context.Context, log *slog.Logger, args []string) error {
	fs := flag.NewFlagSet("http-client", flag.ContinueOnError)
	target := fs.String("target", "", "target URL, e.g. http://10.100.0.11:8080/")
	interval := fs.Duration("interval", time.Second, "request interval per worker")
	timeout := fs.Duration("timeout", 2*time.Second, "request timeout")
	concurrency := fs.Int("concurrency", 1, "number of parallel workers, each issuing one request per interval")
	payloadBytes := fs.Int("payload-bytes", 0, "POST body size in bytes; 0 sends a GET with no body")
	if err := fs.Parse(args); err != nil {
		return err
	}

	if *target == "" {
		return errors.New("http-client: --target is required")
	}

	if *concurrency < 1 {
		return errors.New("http-client: --concurrency must be >= 1")
	}

	payload, err := makePayload(*payloadBytes, maxPayloadBytes)
	if err != nil {
		return fmt.Errorf("http-client: %w", err)
	}

	st := newStats()
	go st.report(ctx, log)

	client := &http.Client{Timeout: *timeout}

	var wg sync.WaitGroup
	for range *concurrency {
		wg.Add(1)
		go func() {
			defer wg.Done()
			runHTTPWorker(ctx, log, client, *target, *interval, payload, st)
		}()
	}

	wg.Wait()

	return nil
}

// runHTTPWorker issues one request per interval until ctx is
// cancelled.
func runHTTPWorker(ctx context.Context, log *slog.Logger, client *http.Client, target string, interval time.Duration, payload []byte, st *stats) {
	ticker := time.NewTicker(interval)
	defer ticker.Stop()

	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			start := time.Now()

			status, err := doRequest(ctx, client, target, payload)
			if err != nil || status >= 400 {
				st.failure()
				log.Warn("request failed", "status", status, "error", err)

				continue
			}

			st.success(time.Since(start))
		}
	}
}

// doRequest performs one request and returns the status code; the
// body is drained so connections are reused. A non-empty payload
// sends a POST with that body instead of a GET.
func doRequest(ctx context.Context, client *http.Client, target string, payload []byte) (int, error) {
	method := http.MethodGet

	var body io.Reader
	if len(payload) > 0 {
		method = http.MethodPost
		body = bytes.NewReader(payload)
	}

	req, err := http.NewRequestWithContext(ctx, method, target, body)
	if err != nil {
		return 0, err
	}

	resp, err := client.Do(req)
	if err != nil {
		return 0, err
	}

	defer func() { _ = resp.Body.Close() }()

	_, _ = io.Copy(io.Discard, resp.Body)

	return resp.StatusCode, nil
}
