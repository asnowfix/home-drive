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
