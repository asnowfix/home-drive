package syncer

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"slices"
	"testing"
	"time"

	"github.com/asnowfix/home-drive/homedrive/internal/store"
)

// issue #152: bisync uses the journal to tell a deletion from a creation,
// instead of pushing or pulling every file present on one side only.

var deleteT0 = time.Date(2026, 4, 28, 12, 0, 0, 0, time.UTC)

// seedBaseline journals rel as last synced in both directions at deleteT0
// with remote MD5 "m0".
func seedBaseline(j *mockJournal, rel string) {
	j.Seed(JournalEntry{
		Path: rel, LocalMtime: deleteT0, RemoteMtime: deleteT0,
		RemoteMD5: "m0", LastOrigin: "remote",
	})
}

func TestBisync_Delete_Cases(t *testing.T) {
	const rel = "d/sub/f.txt"
	later := deleteT0.Add(time.Minute)
	tests := []struct {
		name string
		// setup state: local file mtime (zero = absent), remote object
		// (md5 "" = absent), journal baseline present.
		localMtime  time.Time
		remoteMD5   string
		remoteMtime time.Time
		journaled   bool
		// partial journal entry: remote state only (a conflict-loser entry)
		loserEntry bool

		wantLocal      bool
		wantRemote     bool
		wantCopies     int
		wantDownloads  int
		wantRemoteDel  int
		wantJournal    bool
		wantAuditOps   []string
		wantResolution string
	}{
		{
			name: "remote_deleted_local_unchanged_deletes_local", localMtime: deleteT0,
			journaled: true, wantAuditOps: []string{"pull_delete"},
		},
		{
			name: "local_deleted_remote_unchanged_deletes_remote", remoteMD5: "m0",
			remoteMtime: deleteT0, journaled: true, wantRemoteDel: 1,
			wantAuditOps: []string{"push_delete"},
		},
		{
			name: "remote_deleted_local_modified_keeps_and_pushes", localMtime: later,
			journaled: true, wantLocal: true, wantRemote: true, wantCopies: 1,
			wantJournal: true, wantAuditOps: []string{"conflict", "push"},
			wantResolution: deleteConflictResolution,
		},
		{
			name: "local_deleted_remote_modified_keeps_and_pulls", remoteMD5: "m1",
			remoteMtime: later, journaled: true, wantLocal: true, wantRemote: true,
			wantDownloads: 1, wantJournal: true, wantAuditOps: []string{"conflict", "pull"},
			wantResolution: deleteConflictResolution,
		},
		{
			name: "unjournaled_local_only_is_pushed", localMtime: deleteT0,
			wantLocal: true, wantRemote: true, wantCopies: 1, wantJournal: true,
			wantAuditOps: []string{"push"},
		},
		{
			name: "unjournaled_remote_only_is_pulled", remoteMD5: "m0",
			remoteMtime: deleteT0, wantLocal: true, wantRemote: true,
			wantDownloads: 1, wantJournal: true, wantAuditOps: []string{"pull"},
		},
		{
			name: "entry_without_local_baseline_remote_only_is_pulled", remoteMD5: "m0",
			remoteMtime: deleteT0, loserEntry: true, wantLocal: true, wantRemote: true,
			wantDownloads: 1, wantJournal: true, wantAuditOps: []string{"pull"},
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			root := t.TempDir()
			b, _, remote, journal, _, _ := newTestBisync(t, root, false)
			buf := &threadSafeBuffer{}
			b.auditor = store.NewAuditor(buf, newDiscardLogger())
			seedBystander(t, root, remote, journal)
			if !tc.localMtime.IsZero() {
				createLocalFile(t, root, rel, tc.localMtime)
			}
			if tc.remoteMD5 != "" {
				remote.Seed(rel, tc.remoteMtime, tc.remoteMD5)
			}
			switch {
			case tc.journaled:
				seedBaseline(journal, rel)
			case tc.loserEntry:
				journal.Seed(JournalEntry{Path: rel, RemoteMtime: deleteT0, RemoteMD5: "m0"})
			}

			b.execute(context.Background())

			_, err := os.Lstat(filepath.Join(root, filepath.FromSlash(rel)))
			if got := err == nil; got != tc.wantLocal {
				t.Errorf("local exists = %v, want %v", got, tc.wantLocal)
			}
			if got := remote.HasFile(rel); got != tc.wantRemote {
				t.Errorf("remote exists = %v, want %v", got, tc.wantRemote)
			}
			if got := remote.CopyCount(); got != tc.wantCopies {
				t.Errorf("copies = %d, want %d", got, tc.wantCopies)
			}
			if got := len(remote.downloadedFiles); got != tc.wantDownloads {
				t.Errorf("downloads = %d, want %d", got, tc.wantDownloads)
			}
			if got := len(remote.getDeleteCalls()); got != tc.wantRemoteDel {
				t.Errorf("remote deletes = %d, want %d", got, tc.wantRemoteDel)
			}
			if got := journal.Exists(rel); got != tc.wantJournal {
				t.Errorf("journal entry exists = %v, want %v", got, tc.wantJournal)
			}
			var ops []string
			var resolution string
			for _, l := range parseAuditLines(t, buf) {
				ops = append(ops, l.Op)
				if l.Resolution != "" {
					resolution = l.Resolution
				}
			}
			if !slices.Equal(ops, tc.wantAuditOps) || resolution != tc.wantResolution {
				t.Errorf("audit ops = %v resolution %q, want %v resolution %q",
					ops, resolution, tc.wantAuditOps, tc.wantResolution)
			}
		})
	}
}

// A deleted Drive folder leaves nothing behind locally: files at depth 1-3
// go, and so do the directories that held them.
func TestBisync_Delete_RemovesEmptiedDirs(t *testing.T) {
	root := t.TempDir()
	b, _, remote, journal, _, _ := newTestBisync(t, root, false)
	seedBystander(t, root, remote, journal)
	for _, rel := range []string{"top/a.txt", "top/mid/b.txt", "top/mid/low/c.txt"} {
		createLocalFile(t, root, rel, deleteT0)
		seedBaseline(journal, rel)
	}
	createLocalFile(t, root, "keep/unsynced.txt", deleteT0)

	b.execute(context.Background())

	if _, err := os.Stat(filepath.Join(root, "top")); !os.IsNotExist(err) {
		t.Errorf("top still exists (err=%v)", err)
	}
	// unjournaled local-only content next to it is pushed, never deleted
	if _, err := os.Stat(filepath.Join(root, "keep/unsynced.txt")); err != nil {
		t.Errorf("unsynced file lost: %v", err)
	}
}

// A failed listing must never be read as "everything was deleted".
func TestBisync_Delete_ListFailureDeletesNothing(t *testing.T) {
	root := t.TempDir()
	b, _, remote, journal, _, _ := newTestBisync(t, root, false)
	createLocalFile(t, root, "local.txt", deleteT0)
	seedBaseline(journal, "local.txt")
	seedBaseline(journal, "remote.txt")
	remote.listErr = errors.New("drive: 503 backend error")

	b.execute(context.Background())

	if _, err := os.Stat(filepath.Join(root, "local.txt")); err != nil {
		t.Errorf("local file deleted after listing failure: %v", err)
	}
	if n := len(remote.getDeleteCalls()) + remote.CopyCount() + len(remote.downloadedFiles); n != 0 {
		t.Errorf("%d remote operations after a failed listing, want 0", n)
	}
	if !journal.Exists("local.txt") || !journal.Exists("remote.txt") {
		t.Error("journal entries dropped after listing failure")
	}
}

// Absence from the recursive listing is not proof of deletion: a direct
// lookup must also say not-found, and any other answer keeps the file.
func TestBisync_Delete_ConfirmationGuard(t *testing.T) {
	tests := []struct {
		name    string
		statErr error // nil: the object exists although the listing omitted it
		hidden  bool
	}{
		{name: "listing_omits_object_that_exists", hidden: true},
		{name: "lookup_fails_transiently", statErr: errors.New("drive: 503")},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			root := t.TempDir()
			b, _, remote, journal, _, _ := newTestBisync(t, root, false)
			createLocalFile(t, root, "f.txt", deleteT0)
			seedBaseline(journal, "f.txt")
			remote.statErr = tc.statErr
			if tc.hidden {
				remote.statErr = nil
				remote.files["f.txt"] = RemoteObject{Path: "f.txt", MD5: "m0", ModTime: deleteT0}
				remote.listHidden = map[string]bool{"f.txt": true}
			}

			b.execute(context.Background())

			if _, err := os.Stat(filepath.Join(root, "f.txt")); err != nil {
				t.Errorf("local file deleted without a confirmed remote deletion: %v", err)
			}
			if !journal.Exists("f.txt") {
				t.Error("journal entry dropped")
			}
		})
	}
}

// A path excluded by watcher.exclude is on neither side's view, so it can
// never turn into a deletion, even with a stale journal entry.
func TestBisync_Delete_ExcludedPathUntouched(t *testing.T) {
	tests := []struct {
		name      string
		seedLocal bool
		seedRmt   bool
	}{
		{name: "excluded_local_absent_remote", seedLocal: true},
		{name: "excluded_remote_absent_local", seedRmt: true},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			const rel = "proj/.git/HEAD"
			root := t.TempDir()
			b, _, remote, journal, _, _ := newTestBisync(t, root, false)
			b.cfg.Exclude = []string{"**/.git/**"}
			if tc.seedLocal {
				createLocalFile(t, root, rel, deleteT0)
			}
			if tc.seedRmt {
				remote.Seed(rel, deleteT0, "m0")
			}
			seedBaseline(journal, rel)

			b.execute(context.Background())

			_, err := os.Stat(filepath.Join(root, filepath.FromSlash(rel)))
			if (err == nil) != tc.seedLocal {
				t.Errorf("local presence changed (err=%v)", err)
			}
			if remote.HasFile(rel) != tc.seedRmt {
				t.Error("remote presence changed")
			}
			if n := len(remote.getDeleteCalls()) + remote.CopyCount() + len(remote.downloadedFiles); n != 0 {
				t.Errorf("%d operations on an excluded path, want 0", n)
			}
		})
	}
}

// Dry-run audits the deletions it would make and makes none.
func TestBisync_Delete_DryRun(t *testing.T) {
	root := t.TempDir()
	b, _, remote, journal, _, _ := newTestBisync(t, root, true)
	buf := &threadSafeBuffer{}
	b.auditor = store.NewAuditor(buf, newDiscardLogger())
	createLocalFile(t, root, "gone-remote.txt", deleteT0)
	seedBaseline(journal, "gone-remote.txt")
	remote.Seed("gone-local.txt", deleteT0, "m0")
	seedBaseline(journal, "gone-local.txt")

	b.execute(context.Background())

	if _, err := os.Stat(filepath.Join(root, "gone-remote.txt")); err != nil {
		t.Errorf("dry-run deleted a local file: %v", err)
	}
	if !remote.HasFile("gone-local.txt") || len(remote.getDeleteCalls()) != 0 {
		t.Error("dry-run deleted a remote file")
	}
	if !journal.Exists("gone-remote.txt") || !journal.Exists("gone-local.txt") {
		t.Error("dry-run dropped journal entries")
	}
	ops := map[string]bool{}
	for _, l := range parseAuditLines(t, buf) {
		if !l.DryRun {
			t.Errorf("audit line %+v lacks dry_run", l)
		}
		ops[l.Op+":"+l.Path] = true
	}
	if !ops["pull_delete:gone-remote.txt"] || !ops["push_delete:gone-local.txt"] {
		t.Errorf("audit ops = %v, want both dry-run deletions", ops)
	}
}

// The delete-vs-modify conflict is also published as an MQTT event.
func TestBisync_Delete_ConflictEventPublished(t *testing.T) {
	root := t.TempDir()
	b, _, _, journal, mqtt, _ := newTestBisync(t, root, false)
	createLocalFile(t, root, "f.txt", deleteT0.Add(time.Minute))
	seedBaseline(journal, "f.txt")

	b.execute(context.Background())

	var found bool
	for _, p := range mqtt.published {
		if m, ok := p.(map[string]any); ok && m["type"] == "conflict.detected" &&
			m["path"] == "f.txt" && m["resolution"] == deleteConflictResolution {
			found = true
		}
	}
	if !found {
		t.Errorf("no conflict.detected event, published: %v", mqtt.published)
	}
}

func TestPlannedDeletions_OnlyDeleteKinds(t *testing.T) {
	diffs := []FileDiff{
		{Path: "a", Kind: DiffLocalOnly}, {Path: "b", Kind: DiffRemoteDeleted},
		{Path: "c", Kind: DiffLocalDeleted}, {Path: "d", Kind: DiffConflict},
	}
	got := plannedDeletions(diffs)
	if len(got) != 2 || got[0].Path != "b" || got[1].Path != "c" {
		t.Errorf("plannedDeletions = %+v, want b and c", got)
	}
}
