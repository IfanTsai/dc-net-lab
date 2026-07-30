// Command web serves the built web UI and reverse-proxies API,
// WebSocket and metrics traffic to the controller. It is the
// user-facing entry of a deployment: the browser talks to one origin,
// so the frontend keeps relative URLs and works from any machine; on
// a split install this process simply runs wherever the UI should be
// served from, pointed at the controller with --controller.
package main

import (
	"context"
	"flag"
	"fmt"
	"log/slog"
	"net/http"
	"net/http/httputil"
	"net/url"
	"os"
	"path/filepath"
	"strings"
)

func main() {
	var (
		listen     string
		dir        string
		controller string
		devProxy   string
	)
	flag.StringVar(&listen, "listen", "127.0.0.1:8080", "HTTP address to listen on")
	flag.StringVar(&dir, "web-dir", "web/dist", "directory with the built web UI")
	flag.StringVar(&controller, "controller", "http://127.0.0.1:8180", "controller base URL for /api, /ws and /metrics")
	flag.StringVar(&devProxy, "dev-proxy", "",
		"Vite dev server URL; when set, UI requests proxy there (hot reload) instead of serving web-dir")
	flag.Parse()

	log := slog.New(slog.NewTextHandler(os.Stderr, nil))
	handler, err := newHandler(dir, controller, devProxy)
	if err != nil {
		log.Error("web server init failed", "error", err)
		os.Exit(1)
	}

	log.Info("web server starting", "listen", listen, "controller", controller)
	if err := http.ListenAndServe(listen, handler); err != nil {
		log.Error("web server exited", "error", err)
		os.Exit(1)
	}
}

// newHandler routes /api, /ws and /metrics to the controller and
// everything else to the UI (built files or the Vite dev server).
func newHandler(dir, controller, devProxy string) (http.Handler, error) {
	api, err := proxyTo(controller)
	if err != nil {
		return nil, fmt.Errorf("invalid controller URL %q: %w", controller, err)
	}

	var ui http.Handler
	if devProxy != "" {
		ui, err = proxyTo(devProxy)
		if err != nil {
			return nil, fmt.Errorf("invalid dev proxy URL %q: %w", devProxy, err)
		}
	} else {
		ui = spaHandler(dir)
	}

	mux := http.NewServeMux()
	mux.Handle("/api/", api)
	mux.Handle("/ws/", api)
	mux.Handle("/metrics", api)
	mux.Handle("/", ui)

	return mux, nil
}

// proxyTo builds a reverse proxy to base. Upgraded connections
// (WebSockets: capture feeds, node terminals, Vite HMR) pass through;
// their lifetime is bound to the peers, so the request context is
// detached from the proxy handler.
func proxyTo(base string) (http.Handler, error) {
	target, err := url.Parse(base)
	if err != nil {
		return nil, err
	}

	proxy := httputil.NewSingleHostReverseProxy(target)

	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		proxy.ServeHTTP(w, r.WithContext(context.WithoutCancel(r.Context())))
	}), nil
}

// spaHandler serves the built web UI, falling back to index.html for
// client-side routes such as /topology so deep links work.
func spaHandler(dir string) http.Handler {
	fs := http.FileServer(http.Dir(dir))

	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		path := filepath.Join(dir, filepath.Clean("/"+strings.TrimSuffix(r.URL.Path, "/")))
		if info, err := os.Stat(path); err == nil && !info.IsDir() {
			fs.ServeHTTP(w, r)

			return
		}

		http.ServeFile(w, r, filepath.Join(dir, "index.html"))
	})
}
