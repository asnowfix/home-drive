package syncer

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/asnowfix/home-drive/homedrive/internal/store"
)

// Review round 1 on #152 (R3, R4, S1): the diff is computed before any
// deletion runs, so each deletion re-verifies its premise.

// R3: a local edit made between diff() and syncDiffs() is not deleted.
func TestBisync_Delete_LocalEditedAfterDiffKept(t *testing.T) {
	root := t.TempDir()
	b, _, _, journal, _, _ := newTestBisync(t, root, false)
	createLocalFile(t, root, "f.txt", deleteT0)
	seedBaseline(journal, "f.txt")

	diffs, err := b.diff(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if len(plannedDeletions(diffs)) != 1 {
		t.Fatalf("diff did not plan the deletion: %+v", diffs)
	}
	edit := deleteT0.Add(time.Hour)
	if err := os.Chtimes(filepath.Join(root, "f.txt"), edit, edit); err != nil {
		t.Fatal(err)
	}

	b.syncDiffs(context.Background(), diffs)

	if _, err := os.Stat(filepath.Join(root, "f.txt")); err != nil {
		t.Errorf("file edited after the diff was deleted: %v", err)
	}
	if !journal.Exists("f.txt") {
		t.Error("journal entry dropped")
	}
}

// R4: a Drive edit made after the listing is not trashed; it is pulled back
// with a delete-vs-modify conflict.
func TestBisync_Delete_RemoteEditedAfterListingKept(t *testing.T) {
	root := t.TempDir()
	b, _, remote, journal, _, _ := newTestBisync(t, root, false)
	buf := &threadSafeBuffer{}
	b.auditor = store.NewAuditor(buf, newDiscardLogger())
	remote.Seed("f.txt", deleteT0, "m0")
	seedBaseline(journal, "f.txt")

	diffs, err := b.diff(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if len(plannedDeletions(diffs)) != 1 {
		t.Fatalf("diff did not plan the deletion: %+v", diffs)
	}
	remote.Seed("f.txt", deleteT0.Add(time.Hour), "m1") // edited on Drive

	b.syncDiffs(context.Background(), diffs)

	if len(remote.getDeleteCalls()) != 0 || !remote.HasFile("f.txt") {
		t.Error("edited Drive file was trashed")
	}
	if len(remote.downloadedFiles) != 1 {
		t.Errorf("downloads = %d, want the edited copy pulled back", len(remote.downloadedFiles))
	}
	e, _ := journal.Get("f.txt")
	if e == nil || e.RemoteMD5 != "m1" {
		t.Errorf("journal not re-baselined to the edited copy: %+v", e)
	}
	var conflict bool
	for _, l := range parseAuditLines(t, buf) {
		conflict = conflict || (l.Op == "conflict" && l.Resolution == deleteConflictResolution)
	}
	if !conflict {
		t.Error("no delete-vs-modify conflict audit line")
	}
}

// S1: a local walk failure deletes nothing, on either side.
func TestBisync_Delete_LocalWalkFailureDeletesNothing(t *testing.T) {
	root := filepath.Join(t.TempDir(), "missing-root")
	b, _, remote, journal, _, _ := newTestBisync(t, root, false)
	remote.Seed("f.txt", deleteT0, "m0")
	seedBaseline(journal, "f.txt")

	b.execute(context.Background())

	if len(remote.getDeleteCalls()) != 0 || !remote.HasFile("f.txt") {
		t.Error("remote file deleted after a failed local walk")
	}
	if !journal.Exists("f.txt") {
		t.Error("journal entry dropped after a failed local walk")
	}
}

// S1: the exclusion re-checks inside the delete steps stand on their own,
// even if a diff ever carried an excluded path.
func TestBisync_Delete_ExclusionRecheck(t *testing.T) {
	const rel = "p/.git/HEAD"
	root := t.TempDir()
	b, _, remote, journal, _, _ := newTestBisync(t, root, false)
	b.cfg.Exclude = []string{"**/.git/**"}
	createLocalFile(t, root, rel, deleteT0)
	remote.Seed("p/.git/other", deleteT0, "m0")
	seedBaseline(journal, rel)

	if ok, err := b.deleteLocal(context.Background(), FileDiff{Path: rel, Kind: DiffRemoteDeleted}); ok || err != nil {
		t.Errorf("deleteLocal(excluded) = %v, %v; want false, nil", ok, err)
	}
	if ok, err := b.deleteRemote(context.Background(), FileDiff{Path: "p/.git/other", Kind: DiffLocalDeleted}); ok || err != nil {
		t.Errorf("deleteRemote(excluded) = %v, %v; want false, nil", ok, err)
	}
	if _, err := os.Stat(filepath.Join(root, rel)); err != nil {
		t.Errorf("excluded local file removed: %v", err)
	}
	if len(remote.getDeleteCalls()) != 0 {
		t.Error("excluded remote file deleted")
	}
}

// S1: a failed Stat re-check before trashing keeps the Drive file.
func TestBisync_Delete_RemoteRecheckFailureKeeps(t *testing.T) {
	root := t.TempDir()
	b, _, remote, journal, _, _ := newTestBisync(t, root, false)
	remote.Seed("f.txt", deleteT0, "m0")
	seedBaseline(journal, "f.txt")
	diffs, err := b.diff(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	remote.statErr = errors.New("drive: 503")

	b.syncDiffs(context.Background(), diffs)

	if len(remote.getDeleteCalls()) != 0 || !journal.Exists("f.txt") {
		t.Error("Drive file deleted although the re-check failed")
	}
}

// S2: a dry-run conflict event says so.
func TestBisync_Delete_ConflictEventDryRunFlag(t *testing.T) {
	root := t.TempDir()
	b, _, _, journal, mqtt, _ := newTestBisync(t, root, true)
	createLocalFile(t, root, "f.txt", deleteT0.Add(time.Minute))
	seedBaseline(journal, "f.txt")

	b.execute(context.Background())

	for _, p := range mqtt.published {
		if m, ok := p.(map[string]any); ok && m["type"] == "conflict.detected" {
			if m["dry_run"] != true {
				t.Errorf("dry-run conflict event dry_run = %v, want true", m["dry_run"])
			}
			return
		}
	}
	t.Error("no conflict.detected event published")
}

// S1: delete-vs-modify re-baselines the journal to the surviving copy, and a
// deleted folder drops every entry with one audit line per file.
func TestBisync_Delete_JournalAndAuditAfterPropagation(t *testing.T) {
	t.Run("rebaseline_after_push", func(t *testing.T) {
		root := t.TempDir()
		b, _, _, journal, _, _ := newTestBisync(t, root, false)
		later := deleteT0.Add(time.Minute)
		createLocalFile(t, root, "f.txt", later)
		seedBaseline(journal, "f.txt")

		b.execute(context.Background())

		e, _ := journal.Get("f.txt")
		if e == nil || !e.LocalMtime.Equal(later) || e.RemoteMD5 != md5Hex("content-f.txt") ||
			e.RemoteID == "" {
			t.Errorf("journal = %+v, want LocalMtime %v, uploaded MD5 and remote ID", e, later)
		}
	})
	t.Run("rebaseline_after_pull", func(t *testing.T) {
		root := t.TempDir()
		b, _, remote, journal, _, _ := newTestBisync(t, root, false)
		remote.Seed("f.txt", deleteT0.Add(time.Minute), "m1")
		seedBaseline(journal, "f.txt")

		b.execute(context.Background())

		e, _ := journal.Get("f.txt")
		if e == nil || e.RemoteMD5 != "m1" || e.RemoteID != "id-f.txt" {
			t.Errorf("journal = %+v, want RemoteMD5 m1 and remote ID", e)
		}
	})
	t.Run("folder_deletion_drops_entries_one_audit_each", func(t *testing.T) {
		root := t.TempDir()
		b, _, remote, journal, _, _ := newTestBisync(t, root, false)
		seedBystander(t, root, remote, journal)
		buf := &threadSafeBuffer{}
		b.auditor = store.NewAuditor(buf, newDiscardLogger())
		files := []string{"top/a.txt", "top/mid/b.txt", "top/mid/low/c.txt"}
		for _, rel := range files {
			createLocalFile(t, root, rel, deleteT0)
			seedBaseline(journal, rel)
		}

		b.execute(context.Background())

		seen := map[string]int{}
		for _, l := range parseAuditLines(t, buf) {
			seen[l.Op+":"+l.Path]++
		}
		for _, rel := range files {
			if journal.Exists(rel) {
				t.Errorf("journal entry %s not dropped", rel)
			}
			if seen["pull_delete:"+rel] != 1 {
				t.Errorf("pull_delete audit lines for %s = %d, want 1", rel, seen["pull_delete:"+rel])
			}
		}
	})
}
