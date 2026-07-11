package server

import (
	"net/http"
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
// observations) under /ws/v1 and, when WebDir is set, the built web UI.
func NewHTTPServer(c *conf.Server, svc *service.DCNetLabService, term TerminalOpener, watcher TopologyWatcher, logger log.Logger) *khttp.Server {
	srv := khttp.NewServer(
		khttp.Address(c.HTTPAddr),
		khttp.Middleware(recovery.Recovery(), logging.Server(logger)),
		khttp.RequestDecoder(lenientRequestDecoder),
	)
	v1.RegisterDCNetLabHTTPServer(srv, svc)
	srv.HandleFunc("/ws/v1/labs/{labId}/nodes/{nodeId}/terminal", terminalHandler(term, logger))
	srv.HandleFunc("/ws/v1/labs/{labId}/topology", topologyHandler(watcher))

	if c.WebDir != "" {
		// Registered after the API routes so /api/v1 keeps precedence.
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
