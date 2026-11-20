package http

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"strings"
	"testing"
)

type mockDeleteOverrider struct {
	called int
	err    error
}

func (m *mockDeleteOverrider) AllowNextDeletes(_ context.Context) error {
	m.called++
	return m.err
}

func TestHandleAllowDeletes_Cases(t *testing.T) {
	tests := []struct {
		name       string
		method     string
		overrider  *mockDeleteOverrider // nil: dependency not wired
		wantStatus int
		wantCalls  int
	}{
		{name: "arms_the_override", method: http.MethodPost, overrider: &mockDeleteOverrider{}, wantStatus: http.StatusOK, wantCalls: 1},
		{name: "failure_is_500", method: http.MethodPost, overrider: &mockDeleteOverrider{err: errors.New("boom")}, wantStatus: http.StatusInternalServerError, wantCalls: 1},
		{name: "not_wired_is_501", method: http.MethodPost, wantStatus: http.StatusNotImplemented},
		{name: "get_is_rejected", method: http.MethodGet, overrider: &mockDeleteOverrider{}, wantStatus: http.StatusMethodNotAllowed},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			deps, _, _, _, _, _ := defaultDeps()
			if tc.overrider != nil {
				deps.DeleteOverrider = tc.overrider
			}
			srv, _ := newTestServer(t, deps)

			resp := doRequest(t, srv.Handler(), tc.method, "/bisync/allow-deletes")
			body := readBody(t, resp)

			if resp.StatusCode != tc.wantStatus {
				t.Fatalf("status = %d, want %d (%s)", resp.StatusCode, tc.wantStatus, body)
			}
			if tc.overrider != nil && tc.overrider.called != tc.wantCalls {
				t.Errorf("AllowNextDeletes calls = %d, want %d", tc.overrider.called, tc.wantCalls)
			}
			if tc.wantStatus == http.StatusOK && !strings.Contains(body, "deletes_allowed_once") {
				t.Errorf("body = %s, want deletes_allowed_once", body)
			}
		})
	}
}

func TestHandleStatus_ReportsBisyncDeleteBlock(t *testing.T) {
	deps, _, _, _, sp, _ := defaultDeps()
	sp.info.BisyncDeleteBlocked = &DeleteBlock{
		Planned: 60, Threshold: 16, Reason: "threshold", Since: "2026-11-20T08:00:00Z",
	}
	srv, _ := newTestServer(t, deps)

	resp := doRequest(t, srv.Handler(), http.MethodGet, "/status")
	var got map[string]any
	if err := json.Unmarshal([]byte(readBody(t, resp)), &got); err != nil {
		t.Fatal(err)
	}
	blk, _ := got["bisync_delete_blocked"].(map[string]any)
	if blk["planned"] != float64(60) || blk["threshold"] != float64(16) ||
		blk["reason"] != "threshold" || blk["since"] != "2026-11-20T08:00:00Z" {
		t.Errorf("bisync_delete_blocked = %v", blk)
	}

	sp.info.BisyncDeleteBlocked = nil
	resp = doRequest(t, srv.Handler(), http.MethodGet, "/status")
	if strings.Contains(readBody(t, resp), "bisync_delete_blocked") {
		t.Error("bisync_delete_blocked present while not blocked")
	}
}
