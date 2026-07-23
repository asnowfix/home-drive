package rcloneclient

import (
	"context"
	"log/slog"
	"net/http"
	"sort"
	"strings"
	"testing"

	rclonefs "github.com/rclone/rclone/fs"
	drivev3 "google.golang.org/api/drive/v3"
)

func newTestFullWalkFS() *fakeListingFS {
	return &fakeListingFS{tree: map[string][]rclonefs.DirEntry{
		"": {
			fakeObject{remote: "top.txt", size: 10, id: "top-id"},
			fakeDirectory{remote: "sub", id: "sub-id"},
			fakeDirectory{remote: ".git", id: "git-id"},
		},
		"sub": {
			fakeObject{remote: "sub/child.txt", size: 5, id: "child-id"},
		},
		".git": {
			fakeObject{remote: ".git/HEAD", size: 1, id: "head-id"},
		},
	}}
}

func TestFullWalkThenResume_WalksAndSeedsCache(t *testing.T) {
	r := &RcloneFS{
		log:       slog.Default(),
		pathCache: newIDPathCache(),
		fsObj:     newTestFullWalkFS(),
	}

	changes, err := r.fullWalkThenResume(context.Background(), "resume-tok")
	if err != nil {
		t.Fatalf("fullWalkThenResume: %v", err)
	}
	if changes.NextPageToken != "resume-tok" {
		t.Errorf("NextPageToken = %q, want resume-tok", changes.NextPageToken)
	}

	var paths []string
	for _, ch := range changes.Items {
		paths = append(paths, ch.Path)
	}
	sort.Strings(paths)
	want := []string{".git/HEAD", "sub/child.txt", "top.txt"}
	if len(paths) != len(want) {
		t.Fatalf("paths = %v, want %v", paths, want)
	}
	for i := range want {
		if paths[i] != want[i] {
			t.Errorf("paths = %v, want %v", paths, want)
			break
		}
	}

	for id, wantPath := range map[string]string{
		"top-id": "top.txt", "sub-id": "sub", "child-id": "sub/child.txt",
	} {
		if p, ok := r.pathCache.get(id); !ok || p != wantPath {
			t.Errorf("pathCache[%q] = (%q, %v), want (%q, true)", id, p, ok, wantPath)
		}
	}
}

func TestFullWalkThenResume_ExcludesPatterns(t *testing.T) {
	r := &RcloneFS{
		log:       slog.Default(),
		pathCache: newIDPathCache(),
		exclude:   []string{"**/.git/**"},
		fsObj:     newTestFullWalkFS(),
	}

	changes, err := r.fullWalkThenResume(context.Background(), "resume-tok")
	if err != nil {
		t.Fatalf("fullWalkThenResume: %v", err)
	}
	for _, ch := range changes.Items {
		if ch.Path == ".git/HEAD" {
			t.Errorf("expected .git/HEAD to be excluded, got items %+v", changes.Items)
		}
	}
	// The excluded directory should not have been recursed into at all.
	if _, ok := r.pathCache.get("head-id"); ok {
		t.Errorf("expected .git/HEAD to never be visited (directory excluded)")
	}
}

func TestFullWalkThenResume_EmptyResumeTokenFetchesStartToken(t *testing.T) {
	svc := newTestDriveService(t, func(w http.ResponseWriter, req *http.Request) {
		writeJSON(t, w, &drivev3.StartPageToken{StartPageToken: "fresh-token"})
	})

	r := &RcloneFS{
		log:        slog.Default(),
		pathCache:  newIDPathCache(),
		fsObj:      newTestFullWalkFS(),
		changesSvc: svc,
	}

	changes, err := r.fullWalkThenResume(context.Background(), "")
	if err != nil {
		t.Fatalf("fullWalkThenResume: %v", err)
	}
	if changes.NextPageToken != "fresh-token" {
		t.Errorf("NextPageToken = %q, want fresh-token (unprefixed)", changes.NextPageToken)
	}
}

func TestListChanges_DispatchesOnTokenShape(t *testing.T) {
	cases := []struct {
		name         string
		token        string
		wantFullWalk bool
	}{
		{"empty token triggers full walk", "", true},
		{"prefixed token triggers full walk", initialSyncPrefix + "abc", true},
		{"real token triggers incremental poll", "abc123", false},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			walkCalled := false
			pollCalled := false

			svc := newTestDriveService(t, func(w http.ResponseWriter, req *http.Request) {
				if strings.Contains(req.URL.Path, "changes") && !strings.Contains(req.URL.Path, "startPageToken") {
					pollCalled = true
					writeJSON(t, w, &drivev3.ChangeList{NewStartPageToken: "next"})
					return
				}
				writeJSON(t, w, &drivev3.StartPageToken{StartPageToken: "fresh"})
			})

			fs := newTestFullWalkFS()
			r := &RcloneFS{
				log:        slog.Default(),
				pathCache:  newIDPathCache(),
				fsObj:      fs,
				changesSvc: svc,
				rootID:     "root-id",
			}
			r.pathCache.put("root-id", "")

			_, err := r.ListChanges(context.Background(), tc.token)
			if err != nil {
				t.Fatalf("ListChanges: %v", err)
			}

			// A full walk touches fsObj.List; detect it indirectly via the
			// path cache picking up the fake tree's known IDs.
			_, walked := r.pathCache.get("top-id")
			walkCalled = walked

			if walkCalled != tc.wantFullWalk {
				t.Errorf("walkCalled = %v, want %v", walkCalled, tc.wantFullWalk)
			}
			if pollCalled == tc.wantFullWalk {
				t.Errorf("pollCalled = %v, want %v", pollCalled, !tc.wantFullWalk)
			}
		})
	}
}

// TestListChanges_RemoteOnlyExcludedFileNotPulled exercises the exact
// public entrypoint syncer.Puller calls in production (see
// rclonefs.go:ListChanges) with a file that exists only on the remote
// (".git/HEAD" in the fake tree below has no local counterpart at all) and
// matches a watcher.exclude pattern. It must never appear in the returned
// Items -- if it did, the Puller would download it to the local root on
// the next poll, which is exactly what issue #54's filter-parity
// requirement forbids (PLAN.md §7, §10; see also
// homedrive/docs/migrating-rclone-filters.md).
func TestListChanges_RemoteOnlyExcludedFileNotPulled(t *testing.T) {
	r := &RcloneFS{
		log:       slog.Default(),
		pathCache: newIDPathCache(),
		exclude:   []string{"**/.git/**"},
		fsObj:     newTestFullWalkFS(),
	}

	// A prefixed token (already minted by a prior GetStartPageToken call)
	// triggers a full walk without needing a real OAuth-backed Drive
	// client, the same path a brand-new agent takes on first sync per
	// PLAN.md §7.1.
	changes, err := r.ListChanges(context.Background(), initialSyncPrefix+"resume-tok")
	if err != nil {
		t.Fatalf("ListChanges: %v", err)
	}

	for _, ch := range changes.Items {
		if strings.HasPrefix(ch.Path, ".git") {
			t.Fatalf("excluded remote-only path %q was returned by ListChanges and would be pulled", ch.Path)
		}
	}

	// Non-excluded remote-only files must still come through.
	var sawTop, sawChild bool
	for _, ch := range changes.Items {
		switch ch.Path {
		case "top.txt":
			sawTop = true
		case "sub/child.txt":
			sawChild = true
		}
	}
	if !sawTop || !sawChild {
		t.Errorf("expected non-excluded files to be pulled, got items %+v", changes.Items)
	}
}
