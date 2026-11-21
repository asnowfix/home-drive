package syncer

import (
	"context"
	"encoding/json"
	"strings"
	"testing"
	"time"

	"github.com/asnowfix/home-drive/homedrive/internal/store"
)

// parseAuditLines decodes the per-file JSONL lines written to buf.
func parseAuditLines(t *testing.T, buf *threadSafeBuffer) []store.AuditEntry {
	t.Helper()
	var out []store.AuditEntry
	for _, line := range strings.Split(strings.TrimSpace(buf.String()), "\n") {
		if line == "" {
			continue
		}
		var e store.AuditEntry
		if err := json.Unmarshal([]byte(line), &e); err != nil {
			t.Fatalf("bad audit line %q: %v", line, err)
		}
		out = append(out, e)
	}
	return out
}

func TestBisync_AuditFile_PushPullConflict(t *testing.T) {
	later := classifyT0.Add(time.Hour)
	tests := []struct {
		name       string
		setup      func(t *testing.T, root string, remote *mockRemoteFS)
		wantOp     string
		wantRes    string
		wantOldSet bool
	}{
		{
			name: "local_only_is_push",
			setup: func(t *testing.T, root string, _ *mockRemoteFS) {
				createLocalFile(t, root, "f.txt", classifyT0)
			},
			wantOp: "push",
		},
		{
			name: "remote_only_is_pull",
			setup: func(_ *testing.T, _ string, remote *mockRemoteFS) {
				remote.Seed("f.txt", classifyT0, "m1")
			},
			wantOp: "pull",
		},
		{
			name: "conflict_remote_wins",
			setup: func(t *testing.T, root string, remote *mockRemoteFS) {
				createLocalFile(t, root, "f.txt", classifyT0)
				remote.Seed("f.txt", later, "m1")
			},
			wantOp: "conflict", wantRes: "newer_wins:remote", wantOldSet: true,
		},
		{
			name: "conflict_local_wins",
			setup: func(t *testing.T, root string, remote *mockRemoteFS) {
				createLocalFile(t, root, "f.txt", later)
				remote.Seed("f.txt", classifyT0, "m1")
			},
			wantOp: "conflict", wantRes: "newer_wins:local", wantOldSet: true,
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			root := t.TempDir()
			b, _, remote, _, _, _ := newTestBisync(t, root, false)
			buf := &threadSafeBuffer{}
			b.auditor = store.NewAuditor(buf, newDiscardLogger())
			tc.setup(t, root, remote)

			b.execute(context.Background())

			lines := parseAuditLines(t, buf)
			if len(lines) != 1 {
				t.Fatalf("got %d per-file audit lines, want 1: %v", len(lines), lines)
			}
			e := lines[0]
			if e.Op != tc.wantOp || e.Path != "f.txt" || e.Resolution != tc.wantRes {
				t.Errorf("entry = %+v, want op=%s resolution=%q", e, tc.wantOp, tc.wantRes)
			}
			if (e.OldPath != "") != tc.wantOldSet {
				t.Errorf("old_path = %q, want set=%v", e.OldPath, tc.wantOldSet)
			}
		})
	}
}
