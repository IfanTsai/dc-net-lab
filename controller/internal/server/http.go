package server

import (
	"net/http"

	"github.com/go-kratos/kratos/v2/log"
	"github.com/go-kratos/kratos/v2/middleware/logging"
	"github.com/go-kratos/kratos/v2/middleware/recovery"
	khttp "github.com/go-kratos/kratos/v2/transport/http"

	"github.com/ifantsai/dcnetlab/controller/internal/conf"
	"github.com/ifantsai/dcnetlab/controller/internal/service"
	v1 "github.com/ifantsai/dcnetlab/pb/dcnetlab/v1"
)

// NewHTTPServer builds the Kratos HTTP server serving the protobuf
// API under /api/v1, the WebSocket endpoints (node terminal, topology
// observations) under /ws/v1 and the Prometheus scrape endpoint under
// /metrics. The web UI lives in its own server (web/server), which
// reverse-proxies these routes; the controller is API-only.
func NewHTTPServer(c *conf.Server, svc *service.DCNetLabService, term TerminalOpener, watcher TopologyWatcher, msrc MetricsSource, capfeed CaptureFeed, logger log.Logger) *khttp.Server {
	srv := khttp.NewServer(
		khttp.Address(c.HTTPAddr),
		khttp.Middleware(recovery.Recovery(), logging.Server(logger)),
		khttp.RequestDecoder(lenientRequestDecoder),
	)
	v1.RegisterDCNetLabHTTPServer(srv, svc)
	srv.HandleFunc("/ws/v1/labs/{labId}/nodes/{nodeId}/terminal", terminalHandler(term, logger))
	srv.HandleFunc("/ws/v1/labs/{labId}/topology", topologyHandler(watcher))
	srv.HandleFunc("/ws/v1/labs/{labId}/captures/{id}", captureHandler(capfeed))
	srv.HandleFunc("/api/v1/labs/{labId}/captures/{id}/pcap", capturePcapHandler(capfeed))
	srv.HandleFunc("/metrics", metricsHandler(msrc))

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
