package http

import (
	"context"
	"io"
	"log/slog"
	"net"
	"net/http"
	"testing"
	"time"
)

func TestServer_GracefulShutdown(t *testing.T) {
	deps, _, _, _, _, _ := defaultDeps()
	log := slog.New(slog.NewJSONHandler(io.Discard, nil))
	m := NewMetrics()
	cfg := ServerConfig{ListenAddr: "127.0.0.1:0", EnableMetrics: true}
	srv := NewServer(cfg, deps, m, log)

	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("listen: %v", err)
	}
	addr := ln.Addr().String()

	errCh := make(chan error, 1)
	go func() {
		errCh <- srv.Serve(ln)
	}()

	// Wait for the server to be ready.
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		conn, connErr := net.DialTimeout("tcp", addr, 50*time.Millisecond)
		if connErr == nil {
			conn.Close()
			break
		}
		time.Sleep(10 * time.Millisecond)
	}

	// Verify the server responds.
	resp, err := http.Get("http://" + addr + "/healthz")
	if err != nil {
		t.Fatalf("GET /healthz: %v", err)
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("expected 200, got %d", resp.StatusCode)
	}

	// Shut down gracefully.
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if err := srv.Shutdown(ctx); err != nil {
		t.Fatalf("shutdown: %v", err)
	}

	// Serve should return nil (http.ErrServerClosed is swallowed).
	if err := <-errCh; err != nil {
		t.Fatalf("serve returned unexpected error: %v", err)
	}
}
