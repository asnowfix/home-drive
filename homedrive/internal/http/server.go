package http

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"net"
	"net/http"
	"time"
)

// ServerConfig holds configuration for the HTTP control endpoint.
type ServerConfig struct {
	// ListenAddr is the address to bind (default "127.0.0.1:6090").
	ListenAddr string
	// EnableMetrics controls whether GET /metrics is active.
	EnableMetrics bool
}

// Deps groups the component interfaces the server controls.
type Deps struct {
	Pausable       Pausable
	Resyncable     Resyncable
	Reloadable     Reloadable
	StatusProvider StatusProvider
	HealthChecker  HealthChecker
}

// Server is the HTTP control endpoint for the homedrive agent.
type Server struct {
	cfg     ServerConfig
	deps    Deps
	metrics *Metrics
	log     *slog.Logger
	srv     *http.Server
}

// NewServer creates a Server with the given config, dependencies, and logger.
// If cfg.ListenAddr is empty, it defaults to "127.0.0.1:6090".
func NewServer(cfg ServerConfig, deps Deps, metrics *Metrics, log *slog.Logger) *Server {
	if cfg.ListenAddr == "" {
		cfg.ListenAddr = "127.0.0.1:6090"
	}
	if metrics == nil {
		metrics = NewMetrics()
	}

	s := &Server{
		cfg:     cfg,
		deps:    deps,
		metrics: metrics,
		log:     log,
	}
	s.srv = &http.Server{
		Addr:              cfg.ListenAddr,
		Handler:           s.routes(),
		ReadHeaderTimeout: 10 * time.Second,
	}
	return s
}

// ListenAndServe starts the HTTP server. It blocks until the server is shut
// down or an error occurs. Use Shutdown to stop gracefully.
func (s *Server) ListenAndServe() error {
	s.log.Info("http server starting", "addr", s.cfg.ListenAddr)
	err := s.srv.ListenAndServe()
	if err != nil && err != http.ErrServerClosed {
		return fmt.Errorf("http listen: %w", err)
	}
	return nil
}

// Serve accepts connections on the given listener. Useful for tests.
func (s *Server) Serve(ln net.Listener) error {
	s.log.Info("http server starting", "addr", ln.Addr().String())
	err := s.srv.Serve(ln)
	if err != nil && err != http.ErrServerClosed {
		return fmt.Errorf("http serve: %w", err)
	}
	return nil
}

// Shutdown gracefully shuts down the HTTP server.
func (s *Server) Shutdown(ctx context.Context) error {
	s.log.Info("http server shutting down")
	if err := s.srv.Shutdown(ctx); err != nil {
		return fmt.Errorf("http shutdown: %w", err)
	}
	return nil
}

// Handler returns the http.Handler for use in tests with httptest.
func (s *Server) Handler() http.Handler {
	return s.routes()
}

// Metrics returns the server's metrics collector.
func (s *Server) Metrics() *Metrics {
	return s.metrics
}

// routes builds the HTTP mux with method enforcement.
func (s *Server) routes() http.Handler {
	mux := http.NewServeMux()

	mux.HandleFunc("/status", s.methodOnly(http.MethodGet, s.handleStatus))
	mux.HandleFunc("/pause", s.methodOnly(http.MethodPost, s.handlePause))
	mux.HandleFunc("/resume", s.methodOnly(http.MethodPost, s.handleResume))
	mux.HandleFunc("/resync", s.methodOnly(http.MethodPost, s.handleResync))
	mux.HandleFunc("/reload", s.methodOnly(http.MethodPost, s.handleReload))
	mux.HandleFunc("/healthz", s.methodOnly(http.MethodGet, s.handleHealthz))
	if s.cfg.EnableMetrics {
		mux.HandleFunc("/metrics", s.methodOnly(http.MethodGet, s.handleMetrics))
	}

	return mux
}

// methodOnly wraps a handler to enforce a single HTTP method, returning 405
// for any other method.
func (s *Server) methodOnly(method string, handler http.HandlerFunc) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if r.Method != method {
			w.Header().Set("Allow", method)
			http.Error(w, http.StatusText(http.StatusMethodNotAllowed), http.StatusMethodNotAllowed)
			return
		}
		handler(w, r)
	}
}

// writeJSON marshals v as JSON and writes it to the response with the given
// status code. Errors are logged but not returned to the caller.
func (s *Server) writeJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	if err := json.NewEncoder(w).Encode(v); err != nil {
		s.log.Error("failed to encode JSON response", "error", err)
	}
}

// writeError writes a JSON error response.
func (s *Server) writeError(w http.ResponseWriter, status int, msg string) {
	s.writeJSON(w, status, map[string]string{"error": msg})
}
