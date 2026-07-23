package http

import (
	"context"
	"crypto/subtle"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"net"
	"net/http"
	"strings"
	"time"
)

// ErrAuthTokenRequired is returned by NewServer when ListenAddr is bound to
// a non-loopback address but no AuthToken is configured. Running an
// unauthenticated control endpoint reachable off-host is the worse failure
// mode, so the server fails closed instead of starting unauthenticated.
var ErrAuthTokenRequired = errors.New("http: auth_token is required when listen is bound to a non-loopback address")

// ServerConfig holds configuration for the HTTP control endpoint.
type ServerConfig struct {
	// ListenAddr is the address to bind (default "127.0.0.1:6090").
	ListenAddr string
	// EnableMetrics controls whether GET /metrics is active.
	EnableMetrics bool
	// AuthToken, if set, is required as a Bearer token on every request
	// regardless of bind address. If unset and ListenAddr is loopback,
	// requests are unauthenticated (today's zero-config behavior). If
	// unset and ListenAddr is not loopback, NewServer returns
	// ErrAuthTokenRequired.
	AuthToken string
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
// If cfg.ListenAddr is empty, it defaults to "127.0.0.1:6090". Returns
// ErrAuthTokenRequired if cfg.ListenAddr is non-loopback and cfg.AuthToken
// is empty (fail closed; see ErrAuthTokenRequired).
func NewServer(cfg ServerConfig, deps Deps, metrics *Metrics, log *slog.Logger) (*Server, error) {
	if cfg.ListenAddr == "" {
		cfg.ListenAddr = "127.0.0.1:6090"
	}
	if metrics == nil {
		metrics = NewMetrics()
	}
	if cfg.AuthToken == "" && !isLoopbackAddr(cfg.ListenAddr) {
		return nil, fmt.Errorf("%w (listen=%q)", ErrAuthTokenRequired, cfg.ListenAddr)
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
	return s, nil
}

// isLoopbackAddr reports whether addr (a "host:port" listen address, a bare
// host, or "" for empty host meaning all interfaces) resolves to a loopback
// interface. A missing/empty host (e.g. ":6090") binds every interface and
// is therefore treated as non-loopback.
func isLoopbackAddr(addr string) bool {
	host, _, err := net.SplitHostPort(addr)
	if err != nil {
		host = addr
	}
	if host == "" {
		return false
	}
	if strings.EqualFold(host, "localhost") {
		return true
	}
	ip := net.ParseIP(host)
	if ip == nil {
		return false
	}
	return ip.IsLoopback()
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

	return s.withAuth(mux)
}

// withAuth wraps next with Bearer token authentication. When no AuthToken
// is configured, requests pass through unauthenticated (NewServer already
// guarantees this only happens on a loopback ListenAddr). When an
// AuthToken is configured, every request -- regardless of bind address --
// must carry a matching "Authorization: Bearer <token>" header.
func (s *Server) withAuth(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if s.cfg.AuthToken == "" {
			next.ServeHTTP(w, r)
			return
		}
		if !hasValidBearerToken(r.Header.Get("Authorization"), s.cfg.AuthToken) {
			w.Header().Set("WWW-Authenticate", `Bearer realm="homedrive"`)
			http.Error(w, http.StatusText(http.StatusUnauthorized), http.StatusUnauthorized)
			return
		}
		next.ServeHTTP(w, r)
	})
}

// hasValidBearerToken reports whether header is a well-formed
// "Bearer <token>" value matching want, compared in constant time.
func hasValidBearerToken(header, want string) bool {
	const prefix = "Bearer "
	if !strings.HasPrefix(header, prefix) {
		return false
	}
	got := strings.TrimPrefix(header, prefix)
	return subtle.ConstantTimeCompare([]byte(got), []byte(want)) == 1
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
