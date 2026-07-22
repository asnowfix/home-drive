package rcloneclient

import (
	"context"
	"net/http"
	"strings"
	"testing"

	rclonefs "github.com/rclone/rclone/fs"
	drivev3 "google.golang.org/api/drive/v3"
)

// fakeRootFS embeds a nil rclonefs.Fs and overrides only Root(), the single
// method ensureRootID needs. Any other method call would panic, which is
// fine: these tests never exercise them.
type fakeRootFS struct {
	rclonefs.Fs
	root string
}

func (f fakeRootFS) Root() string { return f.root }

func TestResolveRootID_EmptyRootUsesRootAlias(t *testing.T) {
	var gotPath string
	svc := newTestDriveService(t, func(w http.ResponseWriter, req *http.Request) {
		gotPath = req.URL.Path
		writeJSON(t, w, &drivev3.File{Id: "my-drive-root-id"})
	})

	id, err := resolveRootID(context.Background(), svc, "")
	if err != nil {
		t.Fatalf("resolveRootID: %v", err)
	}
	if id != "my-drive-root-id" {
		t.Errorf("id = %q, want my-drive-root-id", id)
	}
	if !strings.Contains(gotPath, "root") {
		t.Errorf("expected request to the root alias, got path %q", gotPath)
	}
}

func TestResolveRootID_SubfolderWalksSegments(t *testing.T) {
	var queries []string
	svc := newTestDriveService(t, func(w http.ResponseWriter, req *http.Request) {
		q := req.URL.Query().Get("q")
		queries = append(queries, q)
		switch {
		case strings.Contains(q, "'HomeDriveSync'") || strings.Contains(q, "HomeDriveSync"):
			writeJSON(t, w, &drivev3.FileList{Files: []*drivev3.File{{Id: "sync-folder-id"}}})
		default:
			t.Fatalf("unexpected query %q", q)
		}
	})

	id, err := resolveRootID(context.Background(), svc, "HomeDriveSync")
	if err != nil {
		t.Fatalf("resolveRootID: %v", err)
	}
	if id != "sync-folder-id" {
		t.Errorf("id = %q, want sync-folder-id", id)
	}
	if len(queries) != 1 {
		t.Errorf("expected exactly one files.list query, got %d: %v", len(queries), queries)
	}
}

func TestResolveRootID_NestedSubfolderWalksEachSegment(t *testing.T) {
	call := 0
	svc := newTestDriveService(t, func(w http.ResponseWriter, req *http.Request) {
		call++
		q := req.URL.Query().Get("q")
		switch call {
		case 1:
			if !strings.Contains(q, "'root' in parents") {
				t.Errorf("call 1 query = %q, want parent 'root'", q)
			}
			writeJSON(t, w, &drivev3.FileList{Files: []*drivev3.File{{Id: "a-id"}}})
		case 2:
			if !strings.Contains(q, "'a-id' in parents") {
				t.Errorf("call 2 query = %q, want parent 'a-id'", q)
			}
			writeJSON(t, w, &drivev3.FileList{Files: []*drivev3.File{{Id: "b-id"}}})
		default:
			t.Fatalf("unexpected extra call %d", call)
		}
	})

	id, err := resolveRootID(context.Background(), svc, "a/b")
	if err != nil {
		t.Fatalf("resolveRootID: %v", err)
	}
	if id != "b-id" {
		t.Errorf("id = %q, want b-id", id)
	}
	if call != 2 {
		t.Errorf("expected 2 API calls, got %d", call)
	}
}

func TestResolveRootID_SegmentNotFound(t *testing.T) {
	svc := newTestDriveService(t, func(w http.ResponseWriter, req *http.Request) {
		writeJSON(t, w, &drivev3.FileList{Files: nil})
	})

	_, err := resolveRootID(context.Background(), svc, "Missing")
	if err == nil {
		t.Fatal("expected an error for a missing folder segment")
	}
}

func TestEscapeDriveQueryValue_Cases(t *testing.T) {
	cases := []struct{ in, want string }{
		{"plain", "plain"},
		{"it's", `it\'s`},
		{`back\slash`, `back\\slash`},
	}
	for _, tc := range cases {
		if got := escapeDriveQueryValue(tc.in); got != tc.want {
			t.Errorf("escapeDriveQueryValue(%q) = %q, want %q", tc.in, got, tc.want)
		}
	}
}

func TestEnsureRootID_CachesAcrossCalls(t *testing.T) {
	calls := 0
	svc := newTestDriveService(t, func(w http.ResponseWriter, req *http.Request) {
		calls++
		writeJSON(t, w, &drivev3.File{Id: "root-once"})
	})

	r := newTestRcloneFS(svc)
	r.fsObj = fakeRootFS{root: ""}

	ctx := context.Background()
	if err := r.ensureRootID(ctx, svc); err != nil {
		t.Fatalf("ensureRootID (1st): %v", err)
	}
	if err := r.ensureRootID(ctx, svc); err != nil {
		t.Fatalf("ensureRootID (2nd): %v", err)
	}
	if calls != 1 {
		t.Errorf("resolveRootID called %d times, want 1 (cached)", calls)
	}
	if p, ok := r.pathCache.get("root-once"); !ok || p != "" {
		t.Errorf("pathCache[root-once] = (%q, %v), want (\"\", true)", p, ok)
	}
}
