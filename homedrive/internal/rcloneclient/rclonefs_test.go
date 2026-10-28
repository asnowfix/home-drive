package rcloneclient

import (
	"context"
	"errors"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	// Test-only: a real local Fs to exercise CopyFile; not linked into the binary.
	_ "github.com/rclone/rclone/backend/local"
	rclonefs "github.com/rclone/rclone/fs"
)

func TestRemoteObjectFromRclone_MapsFields(t *testing.T) {
	mtime := time.Date(2026, 7, 20, 10, 0, 0, 0, time.UTC)
	obj := fakeObject{remote: "dir/file.txt", size: 99, modTime: mtime, id: "id-1"}

	ro := remoteObjectFromRclone(obj)
	if ro.Path != "dir/file.txt" || ro.Size != 99 || !ro.ModTime.Equal(mtime) || ro.RemoteID != "id-1" {
		t.Errorf("remoteObjectFromRclone = %+v, unexpected", ro)
	}
}

func TestRcloneFS_Stat_Cases(t *testing.T) {
	fsys := &fakeListingFS{byPath: map[string]fakeObject{
		"a.txt": {remote: "a.txt", size: 3, id: "id-a"},
	}}
	r := &RcloneFS{log: slog.Default(), fsObj: fsys}

	t.Run("found", func(t *testing.T) {
		ro, err := r.Stat(context.Background(), "a.txt")
		if err != nil {
			t.Fatalf("Stat: %v", err)
		}
		if ro.Path != "a.txt" || ro.Size != 3 {
			t.Errorf("Stat = %+v, unexpected", ro)
		}
	})

	t.Run("not found", func(t *testing.T) {
		if _, err := r.Stat(context.Background(), "missing.txt"); err == nil {
			t.Fatal("expected an error for a missing object")
		}
	})
}

func TestRcloneFS_List(t *testing.T) {
	fsys := &fakeListingFS{tree: map[string][]rclonefs.DirEntry{
		"": {
			fakeObject{remote: "a.txt", size: 1, id: "id-a"},
			fakeDirectory{remote: "sub", id: "sub-id"},
		},
	}}
	r := &RcloneFS{log: slog.Default(), fsObj: fsys}

	objs, err := r.List(context.Background(), "")
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	if len(objs) != 1 || objs[0].Path != "a.txt" {
		t.Errorf("List = %+v, want single entry a.txt (directories excluded)", objs)
	}
}

func TestRcloneFS_Quota_Cases(t *testing.T) {
	used, total := int64(50), int64(200)

	t.Run("with usage", func(t *testing.T) {
		fsys := &fakeListingFS{usage: &rclonefs.Usage{Used: &used, Total: &total}}
		r := &RcloneFS{log: slog.Default(), fsObj: fsys}
		q, err := r.Quota(context.Background())
		if err != nil {
			t.Fatalf("Quota: %v", err)
		}
		if q.Used != used || q.Total != total {
			t.Errorf("Quota = %+v, want Used=%d Total=%d", q, used, total)
		}
	})

	t.Run("unlimited total", func(t *testing.T) {
		fsys := &fakeListingFS{usage: &rclonefs.Usage{Used: &used}}
		r := &RcloneFS{log: slog.Default(), fsObj: fsys}
		q, err := r.Quota(context.Background())
		if err != nil {
			t.Fatalf("Quota: %v", err)
		}
		if q.Total != -1 {
			t.Errorf("Total = %d, want -1 (unlimited)", q.Total)
		}
	})

	t.Run("not supported", func(t *testing.T) {
		fsys := &fakeListingFS{}
		r := &RcloneFS{log: slog.Default(), fsObj: fsys}
		if _, err := r.Quota(context.Background()); err == nil {
			t.Fatal("expected an error when About returns ErrorNotImplemented")
		}
	})
}

func TestRcloneFS_DownloadFile(t *testing.T) {
	mtime := time.Date(2026, 7, 20, 10, 0, 0, 0, time.UTC)
	fsys := &fakeListingFS{byPath: map[string]fakeObject{
		"remote/a.txt": {remote: "remote/a.txt", size: 5, modTime: mtime, content: "hello"},
	}}
	r := &RcloneFS{log: slog.Default(), fsObj: fsys}

	dst := filepath.Join(t.TempDir(), "sub", "a.txt")
	if err := r.DownloadFile(context.Background(), "remote/a.txt", dst); err != nil {
		t.Fatalf("DownloadFile: %v", err)
	}

	got, err := os.ReadFile(dst)
	if err != nil {
		t.Fatalf("read downloaded file: %v", err)
	}
	if string(got) != "hello" {
		t.Errorf("content = %q, want hello", got)
	}
}

func TestRcloneFS_DownloadFile_NotFound(t *testing.T) {
	r := &RcloneFS{log: slog.Default(), fsObj: &fakeListingFS{}}
	dst := filepath.Join(t.TempDir(), "a.txt")
	if err := r.DownloadFile(context.Background(), "missing.txt", dst); err == nil {
		t.Fatal("expected an error for a missing remote object")
	}
}

// TestNewRcloneFS_Ordering_ValidationFailureSkipsBuildFs is the regression
// test for the issue #86 follow-up: PR #96 wired validateOAuthCredential
// into NewRcloneFS, but called rclonefs.NewFs (buildFs here) *before* it, so
// rclone's own eager root-directory lookup failed first with a generic
// error for exactly the broken-credential case validateOAuthCredential
// exists to catch -- confirmed live against the real #87 credential after
// that PR merged. This proves the fix at the call-order level, not just
// that classifyOAuthRefreshErr buckets error codes correctly (which
// oauthvalidate_test.go already covers and which alone did not catch this
// bug): buildFs must never run once validate has rejected the credential,
// and the error returned to the caller must be validate's, not buildFs's.
func TestNewRcloneFS_Ordering_ValidationFailureSkipsBuildFs(t *testing.T) {
	wantErr := errors.New("stand-in for a classified ErrOAuthClientMisconfigured/ErrOAuthTokenInvalid")
	buildFsCalled := false

	validate := func(_ *RcloneFS, _ context.Context) error { return wantErr }
	buildFs := func(context.Context, string) (rclonefs.Fs, error) {
		buildFsCalled = true
		return &fakeListingFS{}, nil
	}

	_, err := newRcloneFS(context.Background(), RcloneFSConfig{Remote: "gdrive:", Log: slog.Default()}, validate, buildFs)

	if !errors.Is(err, wantErr) {
		t.Errorf("newRcloneFS error = %v, want errors.Is(_, wantErr)", err)
	}
	if buildFsCalled {
		t.Error("buildFs (rclonefs.NewFs) was called despite validation rejecting the credential -- " +
			"this is exactly the issue #86 follow-up regression: a broken credential must never reach " +
			"the doomed rclone backend construction that produces the generic, unclassified error")
	}
}

// TestNewRcloneFS_Ordering_ValidationSuccessRunsBuildFs proves the other
// half of the same ordering invariant: once validation passes, buildFs
// (rclonefs.NewFs) still runs, with the exact remote string from cfg.Remote,
// and its result reaches NewRcloneFS's normal post-construction checks (the
// *drive.Fs type assertion below fails for this fake on purpose, proving
// the returned Fs actually flowed through rather than the call being
// skipped or its result discarded).
func TestNewRcloneFS_Ordering_ValidationSuccessRunsBuildFs(t *testing.T) {
	validateCalled := false
	var gotRemote string

	validate := func(_ *RcloneFS, _ context.Context) error { validateCalled = true; return nil }
	buildFs := func(_ context.Context, remote string) (rclonefs.Fs, error) {
		gotRemote = remote
		return &fakeListingFS{}, nil
	}

	_, err := newRcloneFS(context.Background(), RcloneFSConfig{Remote: "gdrive:", Log: slog.Default()}, validate, buildFs)

	if !validateCalled {
		t.Error("validate was never called")
	}
	if gotRemote != "gdrive:" {
		t.Errorf("buildFs called with remote %q, want %q", gotRemote, "gdrive:")
	}
	// fakeListingFS is not a *drive.Fs, so this is expected to fail the
	// post-buildFs type assertion -- that failure is exactly the proof that
	// buildFs's return value reached NewRcloneFS's existing checks.
	if err == nil || !strings.Contains(err.Error(), "not a drive backend") {
		t.Errorf("newRcloneFS error = %v, want a \"not a drive backend\" error proving buildFs's result was used", err)
	}
}

// TestNewRcloneFS_Ordering_DefaultValidateRunsBeforeBuildFs ties the
// abstract ordering proven above to NewRcloneFS's actual default wiring
// (validate=nil, defaulting to (*RcloneFS).validateOAuthCredential): with no
// OAuth token stored for the remote at all, real validateOAuthCredential
// deterministically skips and returns nil without any network call (see
// TestValidateOAuthCredential_NoTokenStored_SkipsAndReturnsNil), so this
// asserts buildFs still runs afterward -- proving the production default,
// not just an injected fake, preserves the order.
func TestNewRcloneFS_Ordering_DefaultValidateRunsBeforeBuildFs(t *testing.T) {
	buildFsCalled := false
	buildFs := func(context.Context, string) (rclonefs.Fs, error) {
		buildFsCalled = true
		return &fakeListingFS{}, nil
	}

	cfg := RcloneFSConfig{Remote: "test-newrclonefs-ordering-no-token:", Log: slog.Default()}
	_, err := newRcloneFS(context.Background(), cfg, nil, buildFs)

	if !buildFsCalled {
		t.Error("buildFs was never called even though the real validateOAuthCredential should have skipped (no token stored) and returned nil")
	}
	if err == nil || !strings.Contains(err.Error(), "not a drive backend") {
		t.Errorf("newRcloneFS error = %v, want a \"not a drive backend\" error proving buildFs's result was used", err)
	}
}

func TestRcloneFS_CopyFile_Cases(t *testing.T) {
	mtime := time.Date(2026, 4, 18, 8, 29, 52, 0, time.UTC)

	cases := []struct {
		name     string
		dstDir   string
		wantPath string
	}{
		{name: "RootDir", dstDir: "", wantPath: "note.md"},
		{name: "NestedDir", dstDir: "a/b", wantPath: "a/b/note.md"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			ctx := context.Background()
			remoteDir := t.TempDir()
			// A real local-backend Fs stands in for Drive; the local
			// backend is imported by this test only, never by the binary.
			fsys, err := rclonefs.NewFs(ctx, remoteDir)
			if err != nil {
				t.Fatalf("NewFs: %v", err)
			}
			r := &RcloneFS{log: slog.Default(), fsObj: fsys}

			src := filepath.Join(t.TempDir(), "note.md")
			if err := os.WriteFile(src, []byte("hello drive"), 0o644); err != nil {
				t.Fatal(err)
			}
			if err := os.Chtimes(src, mtime, mtime); err != nil {
				t.Fatal(err)
			}

			ro, err := r.CopyFile(ctx, src, tc.dstDir)
			if err != nil {
				t.Fatalf("CopyFile: %v", err)
			}
			if ro.Path != tc.wantPath || ro.Size != int64(len("hello drive")) {
				t.Errorf("CopyFile = %+v, want path %q size 11", ro, tc.wantPath)
			}

			dst := filepath.Join(remoteDir, filepath.FromSlash(tc.wantPath))
			got, err := os.ReadFile(dst)
			if err != nil {
				t.Fatalf("reading uploaded file: %v", err)
			}
			if string(got) != "hello drive" {
				t.Errorf("uploaded content = %q, want %q", got, "hello drive")
			}
			info, err := os.Stat(dst)
			if err != nil {
				t.Fatal(err)
			}
			if !info.ModTime().Equal(mtime) {
				t.Errorf("uploaded mtime = %v, want %v", info.ModTime(), mtime)
			}
		})
	}
}

func TestRcloneFS_CopyFile_MissingSource(t *testing.T) {
	fsys, err := rclonefs.NewFs(context.Background(), t.TempDir())
	if err != nil {
		t.Fatalf("NewFs: %v", err)
	}
	r := &RcloneFS{log: slog.Default(), fsObj: fsys}

	_, err = r.CopyFile(context.Background(), filepath.Join(t.TempDir(), "absent.md"), "")
	if !errors.Is(err, os.ErrNotExist) {
		t.Errorf("CopyFile of missing source: got %v, want os.ErrNotExist", err)
	}
}

func TestRcloneFS_DeleteFile_Cases(t *testing.T) {
	tests := []struct {
		name    string
		seed    bool
		dir     bool
		wantErr error
	}{
		{name: "existing object is deleted", seed: true},
		{name: "folder maps to ErrIsDir and survives", dir: true, wantErr: ErrIsDir},
		{name: "missing object maps to ErrNotFound", wantErr: ErrNotFound},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			root := t.TempDir()
			if tt.seed {
				if err := os.WriteFile(filepath.Join(root, "f.txt"), []byte("x"), 0o644); err != nil {
					t.Fatal(err)
				}
			}
			if tt.dir {
				if err := os.Mkdir(filepath.Join(root, "f.txt"), 0o755); err != nil {
					t.Fatal(err)
				}
			}
			fsys, err := rclonefs.NewFs(context.Background(), root)
			if err != nil {
				t.Fatalf("NewFs: %v", err)
			}
			r := &RcloneFS{log: slog.Default(), fsObj: fsys}

			err = r.DeleteFile(context.Background(), "f.txt")
			if !errors.Is(err, tt.wantErr) {
				t.Fatalf("DeleteFile error = %v, want %v", err, tt.wantErr)
			}
			if tt.dir {
				if fi, statErr := os.Stat(filepath.Join(root, "f.txt")); statErr != nil || !fi.IsDir() {
					t.Errorf("folder must survive DeleteFile: %v", statErr)
				}
			}
			if tt.seed {
				if _, statErr := os.Stat(filepath.Join(root, "f.txt")); !os.IsNotExist(statErr) {
					t.Errorf("file still present after DeleteFile: %v", statErr)
				}
			}
		})
	}
}
