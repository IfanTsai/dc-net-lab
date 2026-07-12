package trafficgen

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"os"
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

// RunHTTPClient issues one GET per interval against the target and
// records success rate and latency.
func RunHTTPClient(ctx context.Context, log *slog.Logger, args []string) error {
	fs := flag.NewFlagSet("http-client", flag.ContinueOnError)
	target := fs.String("target", "", "target URL, e.g. http://10.100.0.11:8080/")
	interval := fs.Duration("interval", time.Second, "request interval")
	timeout := fs.Duration("timeout", 2*time.Second, "request timeout")
	if err := fs.Parse(args); err != nil {
		return err
	}

	if *target == "" {
		return errors.New("http-client: --target is required")
	}

	st := newStats()
	go st.report(ctx, log)

	client := &http.Client{Timeout: *timeout}
	ticker := time.NewTicker(*interval)
	defer ticker.Stop()

	for {
		select {
		case <-ctx.Done():
			return nil
		case <-ticker.C:
			start := time.Now()

			resp, err := doGet(ctx, client, *target)
			if err != nil || resp >= 400 {
				st.failure()
				log.Warn("request failed", "status", resp, "error", err)

				continue
			}

			st.success(time.Since(start))
		}
	}
}

// doGet performs one GET and returns the status code; the body is
// drained so connections are reused.
func doGet(ctx context.Context, client *http.Client, target string) (int, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, target, nil)
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
