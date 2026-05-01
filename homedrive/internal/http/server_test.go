package http

import (
	"context"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"testing"
)

// --- mock implementations ---

type mockPausable struct {
	pauseCalled  bool
	resumeCalled bool
	pauseErr     error
	resumeErr    error
}

func (m *mockPausable) Pause(_ context.Context) error  { m.pauseCalled = true; return m.pauseErr }
func (m *mockPausable) Resume(_ context.Context) error  { m.resumeCalled = true; return m.resumeErr }

type mockResyncable struct {
	called bool
	err    error
}

func (m *mockResyncable) ForceResync(_ context.Context) error { m.called = true; return m.err }

type mockReloadable struct {
	called bool
	err    error
}

func (m *mockReloadable) Reload(_ context.Context) error { m.called = true; return m.err }

type mockStatusProvider struct {
	info StatusInfo
	err  error
}

func (m *mockStatusProvider) Status(_ context.Context) (StatusInfo, error) {
	return m.info, m.err
}

type mockHealthChecker struct {
	result HealthResult
	err    error
}

func (m *mockHealthChecker) Healthz(_ context.Context) (HealthResult, error) {
	return m.result, m.err
}

// --- helpers ---

func newTestServer(t *testing.T, deps Deps) (*Server, *Metrics) {
	t.Helper()
	m := NewMetrics()
	log := slog.New(slog.NewJSONHandler(io.Discard, nil))
	cfg := ServerConfig{
		ListenAddr:    "127.0.0.1:0",
		EnableMetrics: true,
	}
	srv := NewServer(cfg, deps, m, log)
	return srv, m
}

func defaultDeps() (Deps, *mockPausable, *mockResyncable, *mockReloadable, *mockStatusProvider, *mockHealthChecker) {
	p := &mockPausable{}
	rs := &mockResyncable{}
	rl := &mockReloadable{}
	sp := &mockStatusProvider{info: StatusInfo{
		State:         "running",
		Version:       "test",
		PendingUp:     3,
		PendingDown:   1,
		QuotaUsedPct:  42,
		UptimeSeconds: 600,
	}}
	hc := &mockHealthChecker{result: HealthResult{
		Healthy: true,
		Components: []ComponentHealth{
			{Name: "oauth", Healthy: true},
			{Name: "mqtt", Healthy: true},
			{Name: "disk", Healthy: true},
		},
	}}
	deps := Deps{
		Pausable:       p,
		Resyncable:     rs,
		Reloadable:     rl,
		StatusProvider: sp,
		HealthChecker:  hc,
	}
	return deps, p, rs, rl, sp, hc
}

func doRequest(t *testing.T, handler http.Handler, method, path string) *http.Response {
	t.Helper()
	req := httptest.NewRequest(method, path, nil)
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)
	return rec.Result()
}

func readBody(t *testing.T, resp *http.Response) string {
	t.Helper()
	defer resp.Body.Close()
	b, err := io.ReadAll(resp.Body)
	if err != nil {
		t.Fatalf("reading response body: %v", err)
	}
	return string(b)
}
