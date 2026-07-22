package rcloneclient

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	drivev3 "google.golang.org/api/drive/v3"
	"google.golang.org/api/option"
)

// newTestDriveService builds a *drivev3.Service pointed at a local
// httptest.Server so tests never call the real Google Drive API (per
// homedrive-test-mocks: no real Google API calls in tests).
func newTestDriveService(t *testing.T, handler http.HandlerFunc) *drivev3.Service {
	t.Helper()
	srv := httptest.NewServer(handler)
	t.Cleanup(srv.Close)

	svc, err := drivev3.NewService(context.Background(),
		option.WithoutAuthentication(),
		option.WithHTTPClient(srv.Client()),
		option.WithEndpoint(srv.URL),
	)
	if err != nil {
		t.Fatalf("build test drive service: %v", err)
	}
	return svc
}

func newTestRcloneFS(svc *drivev3.Service) *RcloneFS {
	return &RcloneFS{
		remote:     "gdrive:",
		remoteName: "gdrive",
		log:        slog.Default(),
		changesSvc: svc,
		pathCache:  newIDPathCache(),
	}
}

func writeJSON(t *testing.T, w http.ResponseWriter, v any) {
	t.Helper()
	w.Header().Set("Content-Type", "application/json")
	if err := json.NewEncoder(w).Encode(v); err != nil {
		t.Fatalf("encode response: %v", err)
	}
}

func TestGetStartPageToken_ReturnsPrefixedRealToken(t *testing.T) {
	svc := newTestDriveService(t, func(w http.ResponseWriter, req *http.Request) {
		if !strings.Contains(req.URL.Path, "changes/startPageToken") {
			t.Fatalf("unexpected request path %q", req.URL.Path)
		}
		writeJSON(t, w, &drivev3.StartPageToken{StartPageToken: "12345"})
	})

	r := newTestRcloneFS(svc)
	tok, err := r.GetStartPageToken(context.Background())
	if err != nil {
		t.Fatalf("GetStartPageToken: %v", err)
	}
	if tok != initialSyncPrefix+"12345" {
		t.Errorf("token = %q, want %q", tok, initialSyncPrefix+"12345")
	}
	if strings.Contains(tok, "initial_sync") == false {
		t.Errorf("expected token to still carry the full-walk marker")
	}
}

func TestPollChanges_IncrementalChanges(t *testing.T) {
	fileMTime := "2026-07-20T10:00:00Z"
	requestCount := 0

	svc := newTestDriveService(t, func(w http.ResponseWriter, req *http.Request) {
		requestCount++
		list := &drivev3.ChangeList{
			NewStartPageToken: "next-token",
			Changes: []*drivev3.Change{
				{
					FileId: "file-1",
					File: &drivev3.File{
						Id:           "file-1",
						Name:         "report.txt",
						Parents:      []string{"root-id"},
						MimeType:     "text/plain",
						ModifiedTime: fileMTime,
						Md5Checksum:  "abc123",
						Size:         42,
					},
				},
			},
		}
		writeJSON(t, w, list)
	})

	r := newTestRcloneFS(svc)
	r.rootID = "root-id"
	r.pathCache.put("root-id", "")

	changes, err := r.pollChanges(context.Background(), "start-token")
	if err != nil {
		t.Fatalf("pollChanges: %v", err)
	}
	if requestCount != 1 {
		t.Fatalf("requestCount = %d, want 1", requestCount)
	}
	if changes.NextPageToken != "next-token" {
		t.Errorf("NextPageToken = %q, want %q", changes.NextPageToken, "next-token")
	}
	if len(changes.Items) != 1 {
		t.Fatalf("len(Items) = %d, want 1", len(changes.Items))
	}
	got := changes.Items[0]
	if got.Path != "report.txt" || got.Deleted {
		t.Errorf("Items[0] = %+v, want path=report.txt deleted=false", got)
	}
	if got.Object == nil || got.Object.MD5 != "abc123" || got.Object.Size != 42 {
		t.Errorf("Items[0].Object = %+v, want MD5=abc123 Size=42", got.Object)
	}
	wantMTime, _ := time.Parse(time.RFC3339, fileMTime)
	if !got.Object.ModTime.Equal(wantMTime) {
		t.Errorf("ModTime = %v, want %v", got.Object.ModTime, wantMTime)
	}
}

func TestPollChanges_NestedFileUsesCachedParent(t *testing.T) {
	svc := newTestDriveService(t, func(w http.ResponseWriter, req *http.Request) {
		writeJSON(t, w, &drivev3.ChangeList{
			NewStartPageToken: "next",
			Changes: []*drivev3.Change{{
				FileId: "file-2",
				File: &drivev3.File{
					Id:       "file-2",
					Name:     "photo.jpg",
					Parents:  []string{"folder-a"},
					MimeType: "image/jpeg",
				},
			}},
		})
	})

	r := newTestRcloneFS(svc)
	r.rootID = "root-id"
	r.pathCache.put("root-id", "")
	r.pathCache.put("folder-a", "Pictures/2026")

	changes, err := r.pollChanges(context.Background(), "tok")
	if err != nil {
		t.Fatalf("pollChanges: %v", err)
	}
	if len(changes.Items) != 1 || changes.Items[0].Path != "Pictures/2026/photo.jpg" {
		t.Fatalf("Items = %+v, want single item Pictures/2026/photo.jpg", changes.Items)
	}
}

func TestPollChanges_DeletionCases(t *testing.T) {
	cases := []struct {
		name   string
		change *drivev3.Change
		seed   map[string]string
		want   []Change // nil means skipped
	}{
		{
			name:   "removed with cached path",
			change: &drivev3.Change{FileId: "gone-1", Removed: true},
			seed:   map[string]string{"gone-1": "old/report.txt"},
			want:   []Change{{Path: "old/report.txt", Deleted: true}},
		},
		{
			name:   "removed without cached path is skipped",
			change: &drivev3.Change{FileId: "unknown-1", Removed: true},
			seed:   nil,
			want:   nil,
		},
		{
			name: "trashed file resolves via parent and is deleted",
			change: &drivev3.Change{
				FileId: "trashed-1",
				File: &drivev3.File{
					Id: "trashed-1", Name: "old.txt",
					Parents: []string{"root-id"}, Trashed: true,
				},
			},
			seed: nil,
			want: []Change{{Path: "old.txt", Deleted: true}},
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			svc := newTestDriveService(t, func(w http.ResponseWriter, req *http.Request) {
				writeJSON(t, w, &drivev3.ChangeList{
					NewStartPageToken: "next",
					Changes:           []*drivev3.Change{tc.change},
				})
			})
			r := newTestRcloneFS(svc)
			r.rootID = "root-id"
			r.pathCache.put("root-id", "")
			for id, p := range tc.seed {
				r.pathCache.put(id, p)
			}

			changes, err := r.pollChanges(context.Background(), "tok")
			if err != nil {
				t.Fatalf("pollChanges: %v", err)
			}
			if len(changes.Items) != len(tc.want) {
				t.Fatalf("Items = %+v, want %+v", changes.Items, tc.want)
			}
			for i, w := range tc.want {
				got := changes.Items[i]
				if got.Path != w.Path || got.Deleted != w.Deleted {
					t.Errorf("Items[%d] = %+v, want %+v", i, got, w)
				}
			}
		})
	}
}

func TestPollChanges_ExcludedPathSkipped(t *testing.T) {
	svc := newTestDriveService(t, func(w http.ResponseWriter, req *http.Request) {
		writeJSON(t, w, &drivev3.ChangeList{
			NewStartPageToken: "next",
			Changes: []*drivev3.Change{{
				FileId: "f1",
				File:   &drivev3.File{Id: "f1", Name: "cache.tmp", Parents: []string{"root-id"}},
			}},
		})
	})
	r := newTestRcloneFS(svc)
	r.exclude = []string{"*.tmp"}
	r.rootID = "root-id"
	r.pathCache.put("root-id", "")

	changes, err := r.pollChanges(context.Background(), "tok")
	if err != nil {
		t.Fatalf("pollChanges: %v", err)
	}
	if len(changes.Items) != 0 {
		t.Errorf("Items = %+v, want empty (excluded)", changes.Items)
	}
}

func TestPollChanges_Pagination(t *testing.T) {
	var seenTokens []string
	svc := newTestDriveService(t, func(w http.ResponseWriter, req *http.Request) {
		tok := req.URL.Query().Get("pageToken")
		seenTokens = append(seenTokens, tok)

		if tok == "page-1" {
			writeJSON(t, w, &drivev3.ChangeList{
				NextPageToken: "page-2",
				Changes: []*drivev3.Change{{
					FileId: "a", File: &drivev3.File{Id: "a", Name: "a.txt", Parents: []string{"root-id"}},
				}},
			})
			return
		}
		writeJSON(t, w, &drivev3.ChangeList{
			NewStartPageToken: "final-token",
			Changes: []*drivev3.Change{{
				FileId: "b", File: &drivev3.File{Id: "b", Name: "b.txt", Parents: []string{"root-id"}},
			}},
		})
	})

	r := newTestRcloneFS(svc)
	r.rootID = "root-id"
	r.pathCache.put("root-id", "")

	changes, err := r.pollChanges(context.Background(), "page-1")
	if err != nil {
		t.Fatalf("pollChanges: %v", err)
	}
	if len(seenTokens) != 2 || seenTokens[0] != "page-1" || seenTokens[1] != "page-2" {
		t.Errorf("seenTokens = %v, want [page-1 page-2]", seenTokens)
	}
	if changes.NextPageToken != "final-token" {
		t.Errorf("NextPageToken = %q, want final-token", changes.NextPageToken)
	}
	if len(changes.Items) != 2 {
		t.Errorf("len(Items) = %d, want 2", len(changes.Items))
	}
}

func TestPollChanges_410Gone_ReturnsErrGone(t *testing.T) {
	svc := newTestDriveService(t, func(w http.ResponseWriter, req *http.Request) {
		w.WriteHeader(http.StatusGone)
		_, _ = fmt.Fprint(w, `{"error":{"code":410,"message":"Invalid page token"}}`)
	})

	r := newTestRcloneFS(svc)
	r.rootID = "root-id"
	r.pathCache.put("root-id", "")

	_, err := r.pollChanges(context.Background(), "stale-token")
	if err == nil {
		t.Fatal("expected an error")
	}
	if !errors.Is(err, ErrGone) {
		t.Errorf("expected error wrapping ErrGone, got: %v", err)
	}
}

func TestIsGoneErr_Cases(t *testing.T) {
	svc := newTestDriveService(t, func(w http.ResponseWriter, req *http.Request) {
		w.WriteHeader(http.StatusGone)
		_, _ = fmt.Fprint(w, `{"error":{"code":410,"message":"gone"}}`)
	})
	_, err := svc.Changes.List("tok").Do()
	if err == nil {
		t.Fatal("expected an error from the fake 410 server")
	}
	if !isGoneErr(err) {
		t.Errorf("isGoneErr(%v) = false, want true", err)
	}
	if isGoneErr(nil) {
		t.Errorf("isGoneErr(nil) = true, want false")
	}
}
