package syncer

import (
	"context"
	"os"
	"path/filepath"
	"testing"
	"time"
)

func TestPuller_FolderChange(t *testing.T) {
	tests := []struct {
		name   string
		dryRun bool
	}{
		{"EmptyFolderNotCreated", false},
		{"EmptyFolderNotCreatedDryRun", true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			localRoot := t.TempDir()
			now := time.Date(2026, 4, 28, 14, 0, 0, 0, time.UTC)

			remote := newMockRemoteFS()
			remote.startToken = "start-1"
			remote.changes["start-1"] = Changes{
				Items: []Change{
					{Path: "empty", Object: &RemoteObject{Path: "empty", RemoteID: "d1", IsDir: true}},
					{Path: "top.txt", Object: &RemoteObject{
						Path: "top.txt", Size: 3, MD5: "m", ModTime: now, RemoteID: "f1"}},
				},
				NextPageToken: "token-2",
			}
			store := newMockStore()
			p := NewPuller(
				PullerConfig{
					Interval: 30 * time.Second, LocalRoot: localRoot,
					ConflictPolicy: PolicyNewerWins, DryRun: tt.dryRun,
				},
				remote, store, newMockAuditLogger(), newMockPublisher(),
				newDiscardLogger(), fixedClock(now),
			)

			if err := p.PollOnce(context.Background()); err != nil {
				t.Fatalf("PollOnce: %v", err)
			}

			// Only files are synced: the empty folder is not mirrored.
			if _, err := os.Stat(filepath.Join(localRoot, "empty")); err == nil {
				t.Error("empty remote folder was created locally")
			}
			// The folder must never be downloaded as a file; the file
			// after it in the same page is still processed (unless dry-run).
			for _, dl := range remote.downloadedFiles {
				if dl.RemotePath == "empty" {
					t.Error("folder was passed to DownloadFile")
				}
			}
			wantDownloads := 1
			if tt.dryRun {
				wantDownloads = 0
			}
			if len(remote.downloadedFiles) != wantDownloads {
				t.Errorf("downloads = %d, want %d", len(remote.downloadedFiles), wantDownloads)
			}
			if _, ok, _ := store.Get(context.Background(), "empty"); ok {
				t.Error("folder must not be journaled")
			}
			if token, _ := store.GetPageToken(context.Background()); token != "token-2" {
				t.Errorf("page token = %q, want token-2", token)
			}
		})
	}
}

// TestPuller_FolderDelete covers issue #114: a Drive folder trash/removal
// reaches processDelete as a Deleted change for the folder's path (the
// ID-to-path cache maps folder IDs too), with no Object to say it is a
// directory.
func TestPuller_FolderDelete(t *testing.T) {
	del := func(p string) Change { return Change{Path: p, Deleted: true} }
	tests := []struct {
		name     string
		files    []string // local files to create under localRoot
		dirs     []string // local directories to create under localRoot
		changes  []Change
		wantGone []string
		wantKept []string
	}{
		{
			name:     "EmptyDirRemoved",
			dirs:     []string{"d"},
			changes:  []Change{del("d")},
			wantGone: []string{"d"},
		},
		{
			name:     "NonEmptyDirKeepsUnsyncedFiles",
			files:    []string{"d/unsynced.txt", "d/sub/deep.txt"},
			changes:  []Change{del("d")},
			wantKept: []string{"d", "d/unsynced.txt", "d/sub/deep.txt"},
		},
		{
			name:     "ChildrenThenParent",
			files:    []string{"d/a.txt", "d/b.txt"},
			changes:  []Change{del("d/a.txt"), del("d/b.txt"), del("d")},
			wantGone: []string{"d/a.txt", "d/b.txt", "d"},
		},
		{
			name:     "ParentThenChildren",
			files:    []string{"d/a.txt"},
			changes:  []Change{del("d"), del("d/a.txt")},
			wantGone: []string{"d/a.txt"},
			wantKept: []string{"d"},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			localRoot := t.TempDir()
			now := time.Date(2026, 4, 28, 14, 0, 0, 0, time.UTC)
			for _, d := range tt.dirs {
				if err := os.MkdirAll(filepath.Join(localRoot, d), 0o755); err != nil {
					t.Fatal(err)
				}
			}
			for _, f := range tt.files {
				fp := filepath.Join(localRoot, f)
				if err := os.MkdirAll(filepath.Dir(fp), 0o755); err != nil {
					t.Fatal(err)
				}
				if err := os.WriteFile(fp, []byte("x"), 0o644); err != nil {
					t.Fatal(err)
				}
			}

			remote := newMockRemoteFS()
			remote.startToken = "start-1"
			remote.changes["start-1"] = Changes{Items: tt.changes, NextPageToken: "token-2"}
			store := newMockStore()
			p := NewPuller(
				PullerConfig{
					Interval: 30 * time.Second, LocalRoot: localRoot,
					ConflictPolicy: PolicyNewerWins,
				},
				remote, store, newMockAuditLogger(), newMockPublisher(),
				newDiscardLogger(), fixedClock(now),
			)

			if err := p.PollOnce(context.Background()); err != nil {
				t.Fatalf("PollOnce: %v", err)
			}
			for _, g := range tt.wantGone {
				if _, err := os.Stat(filepath.Join(localRoot, g)); !os.IsNotExist(err) {
					t.Errorf("%s still exists (err=%v)", g, err)
				}
			}
			for _, k := range tt.wantKept {
				if _, err := os.Stat(filepath.Join(localRoot, k)); err != nil {
					t.Errorf("%s was removed: %v", k, err)
				}
			}
			if token, _ := store.GetPageToken(context.Background()); token != "token-2" {
				t.Errorf("page token = %q, want token-2", token)
			}
		})
	}
}

// TestPuller_FileDeleteStrict checks that the #114 directory tolerance does
// not extend to files: a file that cannot be removed still fails the poll.
func TestPuller_FileDeleteStrict(t *testing.T) {
	tests := []struct {
		name string
		path string
	}{
		{"ReadOnlyParent", "ro/f.txt"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if os.Geteuid() == 0 {
				t.Skip("root bypasses directory permissions")
			}
			localRoot := t.TempDir()
			fp := filepath.Join(localRoot, tt.path)
			if err := os.MkdirAll(filepath.Dir(fp), 0o755); err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(fp, []byte("x"), 0o644); err != nil {
				t.Fatal(err)
			}
			if err := os.Chmod(filepath.Dir(fp), 0o555); err != nil {
				t.Fatal(err)
			}
			t.Cleanup(func() { _ = os.Chmod(filepath.Dir(fp), 0o755) })

			now := time.Date(2026, 4, 28, 14, 0, 0, 0, time.UTC)
			remote := newMockRemoteFS()
			remote.startToken = "start-1"
			remote.changes["start-1"] = Changes{
				Items:         []Change{{Path: tt.path, Deleted: true}},
				NextPageToken: "token-2",
			}
			p := NewPuller(
				PullerConfig{
					Interval: 30 * time.Second, LocalRoot: localRoot,
					ConflictPolicy: PolicyNewerWins,
				},
				remote, newMockStore(), newMockAuditLogger(), newMockPublisher(),
				newDiscardLogger(), fixedClock(now),
			)
			if err := p.PollOnce(context.Background()); err == nil {
				t.Fatal("PollOnce succeeded, want file delete error")
			}
			if _, err := os.Stat(fp); err != nil {
				t.Errorf("file removed despite error: %v", err)
			}
		})
	}
}
