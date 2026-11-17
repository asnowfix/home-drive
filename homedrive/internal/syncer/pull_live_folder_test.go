package syncer

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"testing"
	"time"
)

// U1b (#152 review 3): the folder sweep decides from a change that carries
// only a possibly stale cached path, so it first confirms on Drive that
// nothing lives at that path any more.
func TestPuller_FolderDelete_ConfirmedAgainstDrive(t *testing.T) {
	tests := []struct {
		name      string
		liveFile  string // a file present on Drive under A
		listErr   error
		wantSwept bool
	}{
		{name: "nothing_on_drive_swept", wantSwept: true},
		{name: "live_folder_at_path_not_swept", liveFile: "A/other.txt"},
		{name: "lookup_error_not_swept", listErr: errors.New("drive: 503")},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			root := t.TempDir()
			st := newMockStore()
			createLocalFile(t, root, "A/f.txt", subtreeT0)
			putBaseline(t, st, "A/f.txt", "")
			remote := newMockRemoteFS()
			remote.listErr = tt.listErr
			if tt.liveFile != "" {
				remote.Seed(tt.liveFile, subtreeT0, "m")
			}

			runPullOn(t, remote, root, st, []Change{{Path: "A", Deleted: true, ID: "old-A"}})

			_, err := os.Lstat(filepath.Join(root, "A/f.txt"))
			if swept := os.IsNotExist(err); swept != tt.wantSwept {
				t.Errorf("A/f.txt swept = %v, want %v", swept, tt.wantSwept)
			}
			if _, ok, _ := st.Get(context.Background(), "A/f.txt"); ok == tt.wantSwept {
				t.Errorf("journal entry present = %v, want %v", ok, !tt.wantSwept)
			}
		})
	}
}

// U2 (#152 review 3): a local-wins conflict on the pull path uploads the
// local file; the journal must record the uploaded Drive file's ID, not the
// loser's (now renamed to .old.<N>), or a later Drive delete of the winner
// is judged against the wrong ID.
func TestPuller_LocalWinsConflict_JournalsUploadedID(t *testing.T) {
	root := t.TempDir()
	st := newMockStore()
	createLocalFile(t, root, "f.txt", subtreeT0.Add(time.Hour))
	_ = st.Put(context.Background(), JournalEntry{
		Path: "f.txt", LocalMtime: subtreeT0, RemoteMtime: subtreeT0,
		RemoteMD5: "m0", RemoteID: "loser-id",
	})
	remote := newMockRemoteFS()
	remote.startToken = "t1"
	remote.changes["t1"] = Changes{
		Items: []Change{{Path: "f.txt", Object: &RemoteObject{
			Path: "f.txt", MD5: "m1", ModTime: subtreeT0.Add(time.Minute), RemoteID: "loser-id",
		}}},
		NextPageToken: "t2",
	}
	p := NewPuller(
		PullerConfig{Interval: 30 * time.Second, LocalRoot: root, ConflictPolicy: PolicyNewerWins},
		remote, st, newMockAuditLogger(), newMockPublisher(),
		newDiscardLogger(), fixedClock(subtreeT0.Add(2*time.Hour)),
	)
	if err := p.PollOnce(context.Background()); err != nil {
		t.Fatalf("PollOnce: %v", err)
	}
	if remote.CopyCount() != 1 {
		t.Fatalf("copies = %d, want the local winner uploaded", remote.CopyCount())
	}
	e, _, _ := st.Get(context.Background(), "f.txt")
	if e.RemoteID != "id-f.txt" {
		t.Errorf("journaled RemoteID = %q, want the uploaded file's id-f.txt", e.RemoteID)
	}
}
