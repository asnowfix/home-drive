package http

import (
	"errors"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"testing"
)

// TestIsLoopbackAddr_Cases exercises the bind-address classification used
// to decide whether the fail-closed auth_token requirement applies.
func TestIsLoopbackAddr_Cases(t *testing.T) {
	tests := []struct {
		name string
		addr string
		want bool
	}{
		{"loopback_ipv4_with_port", "127.0.0.1:6090", true},
		{"loopback_ipv4_bare", "127.0.0.1", true},
		{"localhost_with_port", "localhost:6090", true},
		{"localhost_mixed_case", "Localhost:6090", true},
		{"loopback_ipv6_with_port", "[::1]:6090", true},
		{"all_interfaces_with_port", ":6090", false},
		{"all_interfaces_explicit", "0.0.0.0:6090", false},
		{"lan_address", "192.168.1.2:6090", false},
		{"hostname_not_localhost", "nas.local:6090", false},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			if got := isLoopbackAddr(tc.addr); got != tc.want {
				t.Errorf("isLoopbackAddr(%q) = %v, want %v", tc.addr, got, tc.want)
			}
		})
	}
}

// TestNewServer_AuthTokenPolicy covers NewServer's fail-closed behavior:
// loopback + no token is allowed (today's zero-config default); any other
// bind address without a token is rejected with ErrAuthTokenRequired;
// a token always allows construction regardless of bind address.
func TestNewServer_AuthTokenPolicy(t *testing.T) {
	tests := []struct {
		name    string
		listen  string
		token   string
		wantErr bool
	}{
		{"loopback_no_token_allowed", "127.0.0.1:6090", "", false},
		{"loopback_zero_value_no_token_allowed", "", "", false},
		{"non_loopback_no_token_rejected", "0.0.0.0:6090", "", true},
		{"non_loopback_with_token_allowed", "0.0.0.0:6090", "s3cr3t", false},
		{"loopback_with_token_allowed", "127.0.0.1:6090", "s3cr3t", false},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			deps, _, _, _, _, _ := defaultDeps()
			log := slog.New(slog.NewJSONHandler(io.Discard, nil))
			cfg := ServerConfig{ListenAddr: tc.listen, AuthToken: tc.token}

			srv, err := NewServer(cfg, deps, nil, log)
			if tc.wantErr {
				if err == nil {
					t.Fatal("expected error, got nil")
				}
				if !errors.Is(err, ErrAuthTokenRequired) {
					t.Errorf("expected ErrAuthTokenRequired, got %v", err)
				}
				if srv != nil {
					t.Error("expected nil server on error")
				}
				return
			}
			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
			if srv == nil {
				t.Fatal("expected non-nil server")
			}
		})
	}
}

// TestAuthMiddleware_Cases exercises the request-level Bearer token
// enforcement on a loopback server (the only bind address NewServer allows
// without a token), per the acceptance criteria in issue #53.
func TestAuthMiddleware_Cases(t *testing.T) {
	tests := []struct {
		name       string
		token      string // server-configured token; "" disables auth
		authHeader string // request's Authorization header
		wantStatus int
	}{
		{"no_token_configured_no_header_allowed", "", "", http.StatusOK},
		{"no_token_configured_with_header_allowed", "", "Bearer whatever", http.StatusOK},
		{"token_configured_missing_header_rejected", "s3cr3t", "", http.StatusUnauthorized},
		{"token_configured_correct_header_allowed", "s3cr3t", "Bearer s3cr3t", http.StatusOK},
		{"token_configured_wrong_header_rejected", "s3cr3t", "Bearer wrong", http.StatusUnauthorized},
		{"token_configured_malformed_header_rejected", "s3cr3t", "s3cr3t", http.StatusUnauthorized},
		{"token_configured_wrong_scheme_rejected", "s3cr3t", "Basic s3cr3t", http.StatusUnauthorized},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			deps, _, _, _, _, _ := defaultDeps()
			log := slog.New(slog.NewJSONHandler(io.Discard, nil))
			cfg := ServerConfig{ListenAddr: "127.0.0.1:0", AuthToken: tc.token}
			srv, err := NewServer(cfg, deps, nil, log)
			if err != nil {
				t.Fatalf("NewServer: %v", err)
			}

			req := httptest.NewRequest(http.MethodGet, "/status", nil)
			if tc.authHeader != "" {
				req.Header.Set("Authorization", tc.authHeader)
			}
			rec := httptest.NewRecorder()
			srv.Handler().ServeHTTP(rec, req)

			if rec.Code != tc.wantStatus {
				t.Errorf("status = %d, want %d (body: %s)", rec.Code, tc.wantStatus, rec.Body.String())
			}
			if tc.wantStatus == http.StatusUnauthorized {
				if got := rec.Header().Get("WWW-Authenticate"); got == "" {
					t.Error("expected WWW-Authenticate header on 401")
				}
			}
		})
	}
}

// TestAuthMiddleware_AppliesToEveryRoute confirms the auth check runs
// before any handler-specific logic, including POST routes and /healthz.
func TestAuthMiddleware_AppliesToEveryRoute(t *testing.T) {
	deps, _, _, _, _, _ := defaultDeps()
	log := slog.New(slog.NewJSONHandler(io.Discard, nil))
	cfg := ServerConfig{ListenAddr: "127.0.0.1:0", AuthToken: "s3cr3t", EnableMetrics: true}
	srv, err := NewServer(cfg, deps, nil, log)
	if err != nil {
		t.Fatalf("NewServer: %v", err)
	}
	handler := srv.Handler()

	routes := []struct {
		method string
		path   string
	}{
		{http.MethodGet, "/status"},
		{http.MethodPost, "/pause"},
		{http.MethodPost, "/resume"},
		{http.MethodPost, "/resync"},
		{http.MethodPost, "/reload"},
		{http.MethodGet, "/healthz"},
		{http.MethodGet, "/metrics"},
	}

	for _, r := range routes {
		t.Run(r.method+"_"+r.path, func(t *testing.T) {
			req := httptest.NewRequest(r.method, r.path, nil)
			rec := httptest.NewRecorder()
			handler.ServeHTTP(rec, req)
			if rec.Code != http.StatusUnauthorized {
				t.Errorf("%s %s without token: status = %d, want 401", r.method, r.path, rec.Code)
			}
		})
	}
}
