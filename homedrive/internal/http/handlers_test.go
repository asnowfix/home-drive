package http

import (
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"net/http"
	"strings"
	"testing"
)

func TestHandleStatus_ReturnsValidJSON(t *testing.T) {
	deps, _, _, _, sp, _ := defaultDeps()
	sp.info = StatusInfo{
		State:         "running",
		Version:       "v0.1.0",
		PendingUp:     5,
		PendingDown:   2,
		LastPush:      "2026-04-28T10:00:00Z",
		LastPull:      "2026-04-28T10:01:00Z",
		QuotaUsedPct:  55,
		Conflicts24h:  1,
		BytesUp24h:    1024,
		BytesDown24h:  2048,
		DryRun:        false,
		UptimeSeconds: 3600,
	}
	srv, _ := newTestServer(t, deps)
	handler := srv.Handler()

	resp := doRequest(t, handler, http.MethodGet, "/status")
	body := readBody(t, resp)

	if resp.StatusCode != http.StatusOK {
		t.Fatalf("expected 200, got %d", resp.StatusCode)
	}
	if ct := resp.Header.Get("Content-Type"); ct != "application/json" {
		t.Fatalf("expected application/json, got %s", ct)
	}

	var info StatusInfo
	if err := json.Unmarshal([]byte(body), &info); err != nil {
		t.Fatalf("invalid JSON: %v", err)
	}
	if info.State != "running" {
		t.Errorf("state = %q, want %q", info.State, "running")
	}
	if info.Version != "v0.1.0" {
		t.Errorf("version = %q, want %q", info.Version, "v0.1.0")
	}
	if info.PendingUp != 5 {
		t.Errorf("pending_up = %d, want 5", info.PendingUp)
	}
	if info.PendingDown != 2 {
		t.Errorf("pending_down = %d, want 2", info.PendingDown)
	}
	if info.QuotaUsedPct != 55 {
		t.Errorf("quota_used_pct = %d, want 55", info.QuotaUsedPct)
	}
	if info.UptimeSeconds != 3600 {
		t.Errorf("uptime_seconds = %d, want 3600", info.UptimeSeconds)
	}
}

func TestHandleStatus_ProviderError(t *testing.T) {
	deps, _, _, _, sp, _ := defaultDeps()
	sp.err = errors.New("status unavailable")
	srv, _ := newTestServer(t, deps)
	handler := srv.Handler()

	resp := doRequest(t, handler, http.MethodGet, "/status")

	if resp.StatusCode != http.StatusInternalServerError {
		t.Fatalf("expected 500, got %d", resp.StatusCode)
	}
}

func TestHandlePause_ReturnsOKAndCallsMock(t *testing.T) {
	deps, pausable, _, _, _, _ := defaultDeps()
	srv, _ := newTestServer(t, deps)
	handler := srv.Handler()

	resp := doRequest(t, handler, http.MethodPost, "/pause")
	body := readBody(t, resp)

	if resp.StatusCode != http.StatusOK {
		t.Fatalf("expected 200, got %d", resp.StatusCode)
	}
	if !pausable.pauseCalled {
		t.Error("Pause was not called on the mock")
	}
	if !strings.Contains(body, "paused") {
		t.Errorf("body should contain 'paused', got: %s", body)
	}
}

func TestHandlePause_Error(t *testing.T) {
	deps, pausable, _, _, _, _ := defaultDeps()
	pausable.pauseErr = errors.New("pause failed")
	srv, _ := newTestServer(t, deps)
	handler := srv.Handler()

	resp := doRequest(t, handler, http.MethodPost, "/pause")

	if resp.StatusCode != http.StatusInternalServerError {
		t.Fatalf("expected 500, got %d", resp.StatusCode)
	}
}

func TestHandleResume_ReturnsOKAndCallsMock(t *testing.T) {
	deps, pausable, _, _, _, _ := defaultDeps()
	srv, _ := newTestServer(t, deps)
	handler := srv.Handler()

	resp := doRequest(t, handler, http.MethodPost, "/resume")
	body := readBody(t, resp)

	if resp.StatusCode != http.StatusOK {
		t.Fatalf("expected 200, got %d", resp.StatusCode)
	}
	if !pausable.resumeCalled {
		t.Error("Resume was not called on the mock")
	}
	if !strings.Contains(body, "resumed") {
		t.Errorf("body should contain 'resumed', got: %s", body)
	}
}

func TestHandleResume_Error(t *testing.T) {
	deps, pausable, _, _, _, _ := defaultDeps()
	pausable.resumeErr = errors.New("resume failed")
	srv, _ := newTestServer(t, deps)
	handler := srv.Handler()

	resp := doRequest(t, handler, http.MethodPost, "/resume")

	if resp.StatusCode != http.StatusInternalServerError {
		t.Fatalf("expected 500, got %d", resp.StatusCode)
	}
}

func TestHandleResync_Returns202AndTriggers(t *testing.T) {
	deps, _, resyncable, _, _, _ := defaultDeps()
	srv, _ := newTestServer(t, deps)
	handler := srv.Handler()

	resp := doRequest(t, handler, http.MethodPost, "/resync")
	body := readBody(t, resp)

	if resp.StatusCode != http.StatusAccepted {
		t.Fatalf("expected 202, got %d", resp.StatusCode)
	}
	if !resyncable.called {
		t.Error("ForceResync was not called on the mock")
	}
	if !strings.Contains(body, "resync_triggered") {
		t.Errorf("body should contain 'resync_triggered', got: %s", body)
	}
}

func TestHandleResync_Error(t *testing.T) {
	deps, _, resyncable, _, _, _ := defaultDeps()
	resyncable.err = errors.New("resync failed")
	srv, _ := newTestServer(t, deps)
	handler := srv.Handler()

	resp := doRequest(t, handler, http.MethodPost, "/resync")

	if resp.StatusCode != http.StatusInternalServerError {
		t.Fatalf("expected 500, got %d", resp.StatusCode)
	}
}

func TestHandleReload_ReturnsOKAndCallsMock(t *testing.T) {
	deps, _, _, reloadable, _, _ := defaultDeps()
	srv, _ := newTestServer(t, deps)
	handler := srv.Handler()

	resp := doRequest(t, handler, http.MethodPost, "/reload")
	body := readBody(t, resp)

	if resp.StatusCode != http.StatusOK {
		t.Fatalf("expected 200, got %d", resp.StatusCode)
	}
	if !reloadable.called {
		t.Error("Reload was not called on the mock")
	}
	if !strings.Contains(body, "reloaded") {
		t.Errorf("body should contain 'reloaded', got: %s", body)
	}
}

func TestHandleReload_Error(t *testing.T) {
	deps, _, _, reloadable, _, _ := defaultDeps()
	reloadable.err = errors.New("reload failed")
	srv, _ := newTestServer(t, deps)
	handler := srv.Handler()

	resp := doRequest(t, handler, http.MethodPost, "/reload")

	if resp.StatusCode != http.StatusInternalServerError {
		t.Fatalf("expected 500, got %d", resp.StatusCode)
	}
}

func TestHandleHealthz_Healthy(t *testing.T) {
	deps, _, _, _, _, hc := defaultDeps()
	hc.result = HealthResult{
		Healthy: true,
		Components: []ComponentHealth{
			{Name: "oauth", Healthy: true},
			{Name: "mqtt", Healthy: true},
			{Name: "disk", Healthy: true},
		},
	}
	srv, _ := newTestServer(t, deps)
	handler := srv.Handler()

	resp := doRequest(t, handler, http.MethodGet, "/healthz")
	body := readBody(t, resp)

	if resp.StatusCode != http.StatusOK {
		t.Fatalf("expected 200, got %d", resp.StatusCode)
	}

	var result HealthResult
	if err := json.Unmarshal([]byte(body), &result); err != nil {
		t.Fatalf("invalid JSON: %v", err)
	}
	if !result.Healthy {
		t.Error("expected healthy=true")
	}
	if len(result.Components) != 3 {
		t.Errorf("expected 3 components, got %d", len(result.Components))
	}
}

func TestHandleHealthz_Unhealthy(t *testing.T) {
	deps, _, _, _, _, hc := defaultDeps()
	hc.result = HealthResult{
		Healthy: false,
		Components: []ComponentHealth{
			{Name: "oauth", Healthy: false, Message: "token expired"},
			{Name: "mqtt", Healthy: true},
			{Name: "disk", Healthy: true},
		},
	}
	srv, _ := newTestServer(t, deps)
	handler := srv.Handler()

	resp := doRequest(t, handler, http.MethodGet, "/healthz")
	body := readBody(t, resp)

	if resp.StatusCode != http.StatusServiceUnavailable {
		t.Fatalf("expected 503, got %d", resp.StatusCode)
	}

	var result HealthResult
	if err := json.Unmarshal([]byte(body), &result); err != nil {
		t.Fatalf("invalid JSON: %v", err)
	}
	if result.Healthy {
		t.Error("expected healthy=false")
	}
}

func TestHandleHealthz_Error(t *testing.T) {
	deps, _, _, _, _, hc := defaultDeps()
	hc.err = errors.New("check failed")
	srv, _ := newTestServer(t, deps)
	handler := srv.Handler()

	resp := doRequest(t, handler, http.MethodGet, "/healthz")

	if resp.StatusCode != http.StatusInternalServerError {
		t.Fatalf("expected 500, got %d", resp.StatusCode)
	}
}

func TestHandleMetrics_PrometheusFormat(t *testing.T) {
	deps, _, _, _, _, _ := defaultDeps()
	srv, m := newTestServer(t, deps)
	handler := srv.Handler()

	// Seed some metrics.
	m.IncCounter("homedrive_pushes_total")
	m.IncCounter("homedrive_pushes_total")
	m.SetGauge("homedrive_queue_size", 7)

	resp := doRequest(t, handler, http.MethodGet, "/metrics")
	body := readBody(t, resp)

	if resp.StatusCode != http.StatusOK {
		t.Fatalf("expected 200, got %d", resp.StatusCode)
	}
	ct := resp.Header.Get("Content-Type")
	if !strings.HasPrefix(ct, "text/plain") {
		t.Fatalf("expected text/plain content type, got %s", ct)
	}
	if !strings.Contains(body, "# TYPE homedrive_pushes_total counter") {
		t.Errorf("missing counter TYPE line in:\n%s", body)
	}
	if !strings.Contains(body, "homedrive_pushes_total 2") {
		t.Errorf("expected pushes_total=2 in:\n%s", body)
	}
	if !strings.Contains(body, "# TYPE homedrive_queue_size gauge") {
		t.Errorf("missing gauge TYPE line in:\n%s", body)
	}
	if !strings.Contains(body, "homedrive_queue_size 7") {
		t.Errorf("expected queue_size=7 in:\n%s", body)
	}
}

func TestUnknownRoute_Returns404(t *testing.T) {
	deps, _, _, _, _, _ := defaultDeps()
	srv, _ := newTestServer(t, deps)
	handler := srv.Handler()

	resp := doRequest(t, handler, http.MethodGet, "/nonexistent")

	if resp.StatusCode != http.StatusNotFound {
		t.Fatalf("expected 404, got %d", resp.StatusCode)
	}
}

func TestMethodNotAllowed_Returns405(t *testing.T) {
	tests := []struct {
		name   string
		method string
		path   string
	}{
		{name: "GET_pause", method: http.MethodGet, path: "/pause"},
		{name: "GET_resume", method: http.MethodGet, path: "/resume"},
		{name: "GET_resync", method: http.MethodGet, path: "/resync"},
		{name: "GET_reload", method: http.MethodGet, path: "/reload"},
		{name: "POST_status", method: http.MethodPost, path: "/status"},
		{name: "POST_healthz", method: http.MethodPost, path: "/healthz"},
		{name: "POST_metrics", method: http.MethodPost, path: "/metrics"},
		{name: "PUT_pause", method: http.MethodPut, path: "/pause"},
		{name: "DELETE_status", method: http.MethodDelete, path: "/status"},
	}

	deps, _, _, _, _, _ := defaultDeps()
	srv, _ := newTestServer(t, deps)
	handler := srv.Handler()

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			resp := doRequest(t, handler, tc.method, tc.path)

			if resp.StatusCode != http.StatusMethodNotAllowed {
				t.Fatalf("expected 405 for %s %s, got %d", tc.method, tc.path, resp.StatusCode)
			}
			allow := resp.Header.Get("Allow")
			if allow == "" {
				t.Error("expected Allow header to be set")
			}
		})
	}
}

func TestDefaultListenAddr(t *testing.T) {
	deps, _, _, _, _, _ := defaultDeps()
	log := slog.New(slog.NewJSONHandler(io.Discard, nil))
	srv := NewServer(ServerConfig{}, deps, nil, log)
	if srv.cfg.ListenAddr != "127.0.0.1:6090" {
		t.Errorf("default listen addr = %q, want %q", srv.cfg.ListenAddr, "127.0.0.1:6090")
	}
}
