package server

import (
	"context"
	"net/http"
	"net/http/httputil"
	"net/url"
	"os"
	"path/filepath"

	"github.com/go-kratos/kratos/v2/log"
	"github.com/go-kratos/kratos/v2/middleware/logging"
	"github.com/go-kratos/kratos/v2/middleware/recovery"
	khttp "github.com/go-kratos/kratos/v2/transport/http"

	"github.com/ifantsai/dcnetlab/internal/conf"
	"github.com/ifantsai/dcnetlab/internal/service"
	v1 "github.com/ifantsai/dcnetlab/pb/dcnetlab/v1"
)

// NewHTTPServer builds the Kratos HTTP server serving the protobuf
// API under /api/v1, the WebSocket endpoints (node terminal, topology
// observations) under /ws/v1, the Prometheus scrape endpoint under
// /metrics and the web UI: the built files when WebDir is set, or a
// reverse proxy to the Vite dev server when WebDevProxy is set (dev
// mode with hot reload, WebDevProxy wins over WebDir).
func NewHTTPServer(c *conf.Server, svc *service.DCNetLabService, term TerminalOpener, watcher TopologyWatcher, msrc MetricsSource, logger log.Logger) *khttp.Server {
	srv := khttp.NewServer(
		khttp.Address(c.HTTPAddr),
		khttp.Middleware(recovery.Recovery(), logging.Server(logger)),
		khttp.RequestDecoder(lenientRequestDecoder),
	)
	v1.RegisterDCNetLabHTTPServer(srv, svc)
	srv.HandleFunc("/ws/v1/labs/{labId}/nodes/{nodeId}/terminal", terminalHandler(term, logger))
	srv.HandleFunc("/ws/v1/labs/{labId}/topology", topologyHandler(watcher))
	srv.HandleFunc("/metrics", metricsHandler(msrc))

	// The web UI handler is registered after the API routes so /api/v1
	// and /ws/v1 keep precedence.
	switch {
	case c.WebDevProxy != "":
		// Dev mode: forward web requests (including the Vite HMR
		// WebSocket, which connects to the page's own origin) to the
		// local Vite dev server, so the browser stays on the
		// controller port with hot reload.
		target, err := url.Parse(c.WebDevProxy)
		if err != nil {
			log.NewHelper(logger).Errorf("invalid --web-dev-proxy URL %q, web UI disabled: %v", c.WebDevProxy, err)

			break
		}

		// Kratos applies its server timeout (1s by default) to every
		// request context, and ReverseProxy tears down upgraded
		// connections when the context is cancelled — the HMR
		// WebSocket would die after exactly 1s and the Vite client
		// would reload the page in a loop. Detach the context so the
		// proxied connections live as long as the peers keep them.
		proxy := httputil.NewSingleHostReverseProxy(target)
		srv.HandlePrefix("/", http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			proxy.ServeHTTP(w, r.WithContext(context.WithoutCancel(r.Context())))
		}))
	case c.WebDir != "":
		srv.HandlePrefix("/", spaHandler(c.WebDir))
	}

	return srv
}

// lenientRequestDecoder decodes request bodies as JSON when no
// Content-Type is given (e.g. bodyless POSTs from the web UI), where
// the Kratos default decoder would reject the request outright.
func lenientRequestDecoder(r *http.Request, v any) error {
	if r.Header.Get("Content-Type") == "" {
		r.Header.Set("Content-Type", "application/json")
	}

	return khttp.DefaultRequestDecoder(r, v)
}

// spaHandler serves the built web UI, falling back to index.html for
// client-side routes such as /topology so deep links work.
func spaHandler(dir string) http.Handler {
	fs := http.FileServer(http.Dir(dir))

	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		path := filepath.Join(dir, filepath.Clean("/"+r.URL.Path))
		if info, err := os.Stat(path); err == nil && !info.IsDir() {
			fs.ServeHTTP(w, r)

			return
		}

		http.ServeFile(w, r, filepath.Join(dir, "index.html"))
	})
}
