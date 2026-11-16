package syncer

import (
	"context"
	"os"
	"path/filepath"
	"slices"
	"testing"
	"time"
)

// Review round 1 on #152: every pull-side delete, not only the folder sweep,
// must honour the journal (delete-vs-modify, file identity), the sync root
// must be untouchable, and each deletion is audited once.

// runPull feeds changes to a Puller over root/store and returns the audit
// logger and publisher.
func runPull(
	t *testing.T, root string, st *mockStore, changes []Change,
) (*mockAuditLogger, *mockPublisher) {
	t.Helper()
	remote := newMockRemoteFS()
	remote.startToken = "start-1"
	remote.changes["start-1"] = Changes{Items: changes, NextPageToken: "token-2"}
	audit, pub := newMockAuditLogger(), newMockPublisher()
	p := NewPuller(
		PullerConfig{Interval: 30 * time.Second, LocalRoot: root, ConflictPolicy: PolicyNewerWins},
		remote, st, audit, pub, newDiscardLogger(), fixedClock(subtreeT0),
	)
	if err := p.PollOnce(context.Background()); err != nil {
		t.Fatalf("PollOnce: %v", err)
	}
	return audit, pub
}

func putBaseline(t *testing.T, st *mockStore, path, remoteID string) {
	t.Helper()
	if err := st.Put(context.Background(), JournalEntry{
		Path: path, LocalMtime: subtreeT0, RemoteMtime: subtreeT0,
		RemoteMD5: "m", RemoteID: remoteID,
	}); err != nil {
		t.Fatal(err)
	}
}

func touch(t *testing.T, root, rel string, mt time.Time) {
	t.Helper()
	if err := os.Chtimes(filepath.Join(root, rel), mt, mt); err != nil {
		t.Fatal(err)
	}
}

// R1: a file's own delete change (child before parent, or top level) must
// not delete a locally modified file.
func TestPuller_FileDelete_ModifiedKept(t *testing.T) {
	del := func(p string) Change { return Change{Path: p, Deleted: true} }
	tests := []struct {
		name      string
		modified  bool
		journaled bool
		changes   []Change
		path      string
		wantKept  bool
		wantConfl int
	}{
		{name: "child_before_parent_modified", journaled: true, modified: true,
			path: "d/e.txt", changes: []Change{del("d/e.txt"), del("d")}, wantKept: true, wantConfl: 1},
		{name: "top_level_modified", journaled: true, modified: true,
			path: "e.txt", changes: []Change{del("e.txt")}, wantKept: true, wantConfl: 1},
		{name: "child_before_parent_unchanged", journaled: true,
			path: "d/e.txt", changes: []Change{del("d/e.txt"), del("d")}},
		{name: "unjournaled_still_deleted", path: "d/e.txt",
			changes: []Change{del("d/e.txt")}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			root := t.TempDir()
			st := newMockStore()
			createLocalFile(t, root, tt.path, subtreeT0)
			if tt.journaled {
				putBaseline(t, st, tt.path, "")
			}
			if tt.modified {
				touch(t, root, tt.path, subtreeT0.Add(time.Hour))
			}

			audit, pub := runPull(t, root, st, tt.changes)

			_, err := os.Lstat(filepath.Join(root, tt.path))
			if kept := err == nil; kept != tt.wantKept {
				t.Errorf("file kept = %v, want %v", kept, tt.wantKept)
			}
			var confl []string
			for _, e := range audit.getEntries() {
				if e.Op == "conflict" {
					confl = append(confl, e.Path)
				}
			}
			if len(confl) != tt.wantConfl {
				t.Errorf("conflict audit lines = %v, want %d", confl, tt.wantConfl)
			}
			if got := len(pub.getMessagesByTopic(pub.Topic("events", "conflict.detected"))); got != tt.wantConfl {
				t.Errorf("conflict.detected events = %d, want %d", got, tt.wantConfl)
			}
			if tt.wantKept {
				if _, ok, _ := st.Get(context.Background(), tt.path); ok {
					t.Error("journal entry of a kept modified file must be dropped (bisync pushes it)")
				}
			}
		})
	}
}

// R2: a Removed change carrying another file ID than the journaled one is a
// stale mapping and must not delete the live file at that path.
func TestPuller_FileDelete_StaleIDIgnored(t *testing.T) {
	tests := []struct {
		name     string
		journal  string
		changeID string
		wantGone bool
	}{
		{name: "different_id_ignored", journal: "id-new", changeID: "id-old"},
		{name: "same_id_deleted", journal: "id-1", changeID: "id-1", wantGone: true},
		{name: "journal_without_id_deleted", journal: "", changeID: "id-1", wantGone: true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			root := t.TempDir()
			st := newMockStore()
			createLocalFile(t, root, "f.txt", subtreeT0)
			putBaseline(t, st, "f.txt", tt.journal)

			runPull(t, root, st, []Change{{Path: "f.txt", Deleted: true, ID: tt.changeID}})

			_, err := os.Lstat(filepath.Join(root, "f.txt"))
			if gone := os.IsNotExist(err); gone != tt.wantGone {
				t.Errorf("file gone = %v, want %v", gone, tt.wantGone)
			}
			_, has, _ := st.Get(context.Background(), "f.txt")
			if has == tt.wantGone {
				t.Errorf("journal entry present = %v, want %v", has, !tt.wantGone)
			}
		})
	}
}

// R5: the sync root's ID resolves to the empty path; a delete of it must
// never touch local_root, journaled content included.
func TestPuller_DeleteOfSyncRootIgnored(t *testing.T) {
	for _, p := range []string{"", "."} {
		t.Run("path_"+p, func(t *testing.T) {
			root := t.TempDir()
			st := newMockStore()
			createLocalFile(t, root, "a.txt", subtreeT0)
			putBaseline(t, st, "a.txt", "")
			if err := os.MkdirAll(filepath.Join(root, "emptydir"), 0o755); err != nil {
				t.Fatal(err)
			}

			audit, _ := runPull(t, root, st, []Change{{Path: p, Deleted: true}})

			for _, k := range []string{"a.txt", "emptydir"} {
				if _, err := os.Lstat(filepath.Join(root, k)); err != nil {
					t.Errorf("%s removed by a delete of the sync root: %v", k, err)
				}
			}
			if n := len(audit.getEntries()); n != 0 {
				t.Errorf("audit lines = %d, want 0", n)
			}
		})
	}
}

// R7: when Drive reports both a folder and its children, each file is
// audited once.
func TestPuller_FolderAndChildrenAuditedOnce(t *testing.T) {
	root := t.TempDir()
	st := newMockStore()
	for _, f := range []string{"d/a.txt", "d/s/b.txt"} {
		createLocalFile(t, root, f, subtreeT0)
		putBaseline(t, st, f, "")
	}
	audit, _ := runPull(t, root, st, []Change{
		{Path: "d", Deleted: true},
		{Path: "d/a.txt", Deleted: true},
		{Path: "d/s/b.txt", Deleted: true},
		{Path: "d/s", Deleted: true},
	})
	var got []string
	for _, e := range audit.getEntries() {
		if e.Op == "pull_delete" {
			got = append(got, e.Path)
		}
	}
	slices.Sort(got)
	// d/s is pruned by the sweep (directories it removes are not audited)
	want := []string{"d", "d/a.txt", "d/s/b.txt"}
	if !slices.Equal(got, want) {
		t.Errorf("pull_delete audit paths = %v, want one line each: %v", got, want)
	}
}
