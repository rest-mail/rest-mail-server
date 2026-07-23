// Package metricsrv exposes a protocol gateway's process-local Prometheus
// registry over a lightweight HTTP /metrics endpoint.
//
// Each gateway (smtp/imap/pop3) runs as its own process with its own default
// Prometheus registry. The counters the gateway increments — the outbound
// queue worker's messages_sent / pipeline_terminal{outbound}, plus the
// connection-limiter's active_connections / auth_failures — are the package
// vars in internal/metrics, registered into prometheus.DefaultRegisterer via
// that package's init(). This server serves prometheus.DefaultGatherer, the
// gatherer paired with that registry, so those series become scrapeable — the
// same wiring the API process uses (promhttp over the default registry).
package metricsrv

import (
	"context"
	"errors"
	"log/slog"
	"net/http"
	"strconv"
	"time"

	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/promhttp"

	// Blank import guarantees internal/metrics' init() runs so its collectors
	// are registered in the default registry even if a gateway happens not to
	// reference them directly — the handler is then always meaningful.
	_ "github.com/restmail/restmail/internal/metrics"
)

// Handler returns the HTTP handler that serves the process's default Prometheus
// registry in the standard exposition format. It is the exact gatherer the
// gateway metrics are registered into (prometheus.DefaultGatherer).
func Handler() http.Handler {
	return promhttp.HandlerFor(prometheus.DefaultGatherer, promhttp.HandlerOpts{})
}

// Server is a minimal HTTP server that serves GET /metrics and nothing else.
type Server struct {
	srv  *http.Server
	addr string
}

// New builds a metrics server bound to all interfaces on the given port. It is
// meant to be reached over the internal (container) network, not host-published.
// A port <= 0 disables metrics: New returns nil, and Start/Shutdown on a nil
// *Server are no-ops, so callers need no special-casing.
func New(port int) *Server {
	if port <= 0 {
		return nil
	}
	mux := http.NewServeMux()
	mux.Handle("GET /metrics", Handler())
	addr := ":" + strconv.Itoa(port)
	return &Server{
		srv: &http.Server{
			Addr:              addr,
			Handler:           mux,
			ReadHeaderTimeout: 5 * time.Second,
		},
		addr: addr,
	}
}

// Start runs the server in a background goroutine. A clean Shutdown surfaces as
// http.ErrServerClosed, which is expected and not logged as an error.
func (s *Server) Start() {
	if s == nil {
		return
	}
	go func() {
		slog.Info("metrics endpoint listening", "addr", s.addr)
		if err := s.srv.ListenAndServe(); err != nil && !errors.Is(err, http.ErrServerClosed) {
			slog.Error("metrics endpoint failed", "error", err)
		}
	}()
}

// Shutdown gracefully stops the server, bounded by a short timeout.
func (s *Server) Shutdown() {
	if s == nil {
		return
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if err := s.srv.Shutdown(ctx); err != nil {
		slog.Warn("metrics endpoint shutdown error", "error", err)
	}
}
