package rcloneclient

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"strings"
	"sync"
	"testing"

	drivev3 "google.golang.org/api/drive/v3"
)

// flushRecorder is a fake rclone Fs that records DirCacheFlush calls.
type flushRecorder struct {
	fakeRootFS
	flushes *int
}

func (f flushRecorder) DirCacheFlush() { *f.flushes++ }

// fakeDrive answers the three Drive calls RemoveEmptyDir makes.
type fakeDrive struct {
	mu        sync.Mutex
	folders   []string // ids returned for the folder lookup
	children  []string // non-trashed children of the folder
	trashed   []string // children already in the Drive trash
	listErr   bool     // child query fails with HTTP 500
	queries   []string
	patches   []map[string]any
	forbidden []string // any request with a method other than GET/PATCH
}

func (d *fakeDrive) handler(t *testing.T) http.HandlerFunc {
	return func(w http.ResponseWriter, req *http.Request) {
		d.mu.Lock()
		defer d.mu.Unlock()
		switch req.Method {
		case http.MethodGet:
			q := req.URL.Query().Get("q")
			d.queries = append(d.queries, q)
			ids := d.folders
			if strings.HasPrefix(q, "'") { // "'<id>' in parents": the child query
				if d.listErr {
					http.Error(w, "boom", http.StatusInternalServerError)
					return
				}
				// Honour the query like Drive does: trashed children
				// only show up when it does not filter them out.
				ids = d.children
				if !strings.Contains(q, "trashed = false") {
					ids = append(append([]string(nil), ids...), d.trashed...)
				}
			}
			list := &drivev3.FileList{}
			for _, id := range ids {
				list.Files = append(list.Files, &drivev3.File{Id: id})
			}
			writeJSON(t, w, list)
		case http.MethodPatch:
			var body map[string]any
			_ = json.NewDecoder(req.Body).Decode(&body)
			d.patches = append(d.patches, body)
			writeJSON(t, w, &drivev3.File{Id: "fid"})
		default:
			d.forbidden = append(d.forbidden, req.Method+" "+req.URL.Path)
			http.Error(w, "forbidden", http.StatusMethodNotAllowed)
		}
	}
}

func TestRemoveEmptyDir_Cases(t *testing.T) {
	tests := []struct {
		name      string
		path      string
		drive     *fakeDrive
		wantErr   error // nil = success; errAny = some error
		wantTrash bool
	}{
		{name: "empty folder trashed", path: "a/d", drive: &fakeDrive{folders: []string{"fid"}}, wantTrash: true},
		{name: "only trashed children: folder trashed", path: "d",
			drive: &fakeDrive{folders: []string{"fid"}, trashed: []string{"trashed-child"}}, wantTrash: true},
		{name: "non-trashed child refuses", path: "d",
			drive:   &fakeDrive{folders: []string{"fid"}, children: []string{"live-child"}, trashed: []string{"t"}},
			wantErr: ErrDirNotEmpty},
		{name: "missing folder", path: "d", drive: &fakeDrive{}, wantErr: ErrNotFound},
		{name: "duplicate folder names refused", path: "d",
			drive: &fakeDrive{folders: []string{"f1", "f2"}}, wantErr: ErrDirNotEmpty},
		{name: "child listing error never trashes", path: "d",
			drive: &fakeDrive{folders: []string{"fid"}, listErr: true}, wantErr: errAny},
		{name: "root refused", path: "", wantErr: errAny},
		{name: "dot root refused", path: "/./", wantErr: errAny},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			d := tt.drive
			if d == nil {
				d = &fakeDrive{}
			}
			r := newTestRcloneFS(newTestDriveService(t, d.handler(t)))
			r.rootID = "root-id"
			flushes := 0
			r.fsObj = flushRecorder{flushes: &flushes}

			err := r.RemoveEmptyDir(context.Background(), tt.path)
			switch {
			case tt.wantErr == nil && err != nil:
				t.Fatalf("err = %v, want nil", err)
			case tt.wantErr == errAny && err == nil:
				t.Fatal("err = nil, want an error")
			case tt.wantErr != nil && tt.wantErr != errAny && !errors.Is(err, tt.wantErr):
				t.Fatalf("err = %v, want %v", err, tt.wantErr)
			}
			if got := len(d.patches) == 1; got != tt.wantTrash {
				t.Fatalf("trash calls = %d, want trashed = %v", len(d.patches), tt.wantTrash)
			}
			if tt.wantTrash {
				if d.patches[0]["trashed"] != true {
					t.Errorf("update body = %v, want trashed=true", d.patches[0])
				}
				if flushes != 1 {
					t.Errorf("DirCacheFlush calls = %d, want 1", flushes)
				}
			}
			if len(d.forbidden) != 0 {
				t.Errorf("non-trash write requests: %v", d.forbidden)
			}
			for _, q := range d.queries {
				if strings.HasPrefix(q, "'") && !strings.Contains(q, "trashed = false") {
					t.Errorf("child query %q must count only non-trashed children", q)
				}
			}
		})
	}
}

// errAny marks a test case that expects some error, of any kind.
var errAny = errors.New("any error")
