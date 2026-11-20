package trash

import (
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/asnowfix/home-drive/homedrive/internal/store"
)

// plantEntry creates files/<name> and info/<name>.trashinfo in dir, dated at.
func plantEntry(t *testing.T, dir, name, origPath string, at time.Time) {
	t.Helper()
	for _, sub := range []string{"files", "info"} {
		if err := os.MkdirAll(filepath.Join(dir, sub), 0o700); err != nil {
			t.Fatal(err)
		}
	}
	if err := os.WriteFile(filepath.Join(dir, "files", name), []byte("x"), 0o600); err != nil {
		t.Fatal(err)
	}
	info := "[Trash Info]\nPath=" + origPath + "\nDeletionDate=" + at.Format(dateLayout) + "\n"
	if err := os.WriteFile(filepath.Join(dir, "info", name+".trashinfo"), []byte(info), 0o600); err != nil {
		t.Fatal(err)
	}
}

func TestPurge_Cases(t *testing.T) {
	old := testNow.Add(-31 * 24 * time.Hour)
	young := testNow.Add(-29 * 24 * time.Hour)
	tests := []struct {
		name string
		// entries: name -> (deletion date on disk, date in the record, recorded?)
		diskDate   time.Time
		recDate    time.Time
		recorded   bool
		infoGone   bool
		dryRun     bool
		purgeAge   time.Duration
		wantPurged bool
		wantRecord bool
		wantAudit  int
		wantDryRun bool
	}{
		{name: "own_old_entry_purged", diskDate: old, recDate: old, recorded: true,
			purgeAge: 30 * 24 * time.Hour, wantPurged: true, wantAudit: 1},
		{name: "own_young_entry_kept", diskDate: young, recDate: young, recorded: true,
			purgeAge: 30 * 24 * time.Hour, wantRecord: true},
		{name: "foreign_old_entry_untouched", diskDate: old,
			purgeAge: 30 * 24 * time.Hour},
		{name: "own_old_entry_replaced_by_foreign_one_is_untouched", diskDate: young, recDate: old,
			recorded: true, purgeAge: 30 * 24 * time.Hour},
		{name: "own_old_entry_already_restored_drops_record", diskDate: old, recDate: old,
			recorded: true, infoGone: true, purgeAge: 30 * 24 * time.Hour},
		{name: "dry_run_purges_nothing_but_audits", diskDate: old, recDate: old, recorded: true,
			dryRun: true, purgeAge: 30 * 24 * time.Hour, wantRecord: true, wantAudit: 1, wantDryRun: true},
		{name: "age_zero_disables_purge", diskDate: old, recDate: old, recorded: true,
			purgeAge: 0, wantRecord: true},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			e := newEnv(t)
			e.cfg.PurgeAge = tc.purgeAge
			e.cfg.DryRun = tc.dryRun
			dir := filepath.Join(e.root, ".Trash-1000")
			plantEntry(t, dir, "f.txt", e.root+"/f.txt", tc.diskDate)
			if tc.infoGone {
				_ = os.Remove(filepath.Join(dir, "info", "f.txt.trashinfo"))
			}
			if tc.recorded {
				_ = e.recs.PutTrashRecord(store.TrashRecord{Dir: dir, Name: "f.txt",
					Path: e.root + "/f.txt", DeletedAt: tc.recDate})
			}

			n, err := e.trasher().Purge()
			if err != nil {
				t.Fatalf("Purge: %v", err)
			}
			if (n == 1) != (tc.wantPurged || tc.wantDryRun) {
				t.Errorf("purged count = %d", n)
			}
			gone := !exists(filepath.Join(dir, "files", "f.txt"))
			if gone != tc.wantPurged {
				t.Errorf("file removed = %v, want %v", gone, tc.wantPurged)
			}
			if tc.wantPurged && exists(filepath.Join(dir, "info", "f.txt.trashinfo")) {
				t.Error("trashinfo left behind")
			}
			recs, _ := e.recs.ListTrashRecords()
			if (len(recs) == 1) != tc.wantRecord {
				t.Errorf("records = %+v, want record %v", recs, tc.wantRecord)
			}
			lines := e.auditLines()
			if len(lines) != tc.wantAudit {
				t.Fatalf("audit = %+v, want %d line(s)", lines, tc.wantAudit)
			}
			for _, l := range lines {
				if l.Op != "trash_purge" || l.DryRun != tc.wantDryRun {
					t.Errorf("audit line = %+v", l)
				}
			}
		})
	}
}

// Purge touches only recorded entries: a foreign entry next to an own one in
// the same trash directory survives.
func TestPurge_ForeignNeighbourSurvives(t *testing.T) {
	e := newEnv(t)
	old := testNow.Add(-90 * 24 * time.Hour)
	dir := e.home
	plantEntry(t, dir, "mine", e.root+"/mine", old)
	plantEntry(t, dir, "theirs", "/home/u/theirs", old)
	_ = e.recs.PutTrashRecord(store.TrashRecord{Dir: dir, Name: "mine", Path: e.root + "/mine", DeletedAt: old})

	if n, err := e.trasher().Purge(); err != nil || n != 1 {
		t.Fatalf("Purge = %d, %v", n, err)
	}
	if exists(filepath.Join(dir, "files", "mine")) {
		t.Error("own entry not purged")
	}
	if !exists(filepath.Join(dir, "files", "theirs")) || !exists(filepath.Join(dir, "info", "theirs.trashinfo")) {
		t.Error("foreign entry was touched")
	}
}

// A topdir trash entry has a relative Path=; it must still match its record.
func TestPurge_TopdirRelativePath(t *testing.T) {
	e := newEnv(t)
	old := testNow.Add(-90 * 24 * time.Hour)
	dir := filepath.Join(e.root, ".Trash-1000")
	plantEntry(t, dir, "f.txt", "d/f.txt", old)
	_ = e.recs.PutTrashRecord(store.TrashRecord{Dir: dir, Name: "f.txt",
		Path: e.root + "/d/f.txt", DeletedAt: old})
	if n, err := e.trasher().Purge(); err != nil || n != 1 {
		t.Fatalf("Purge = %d, %v", n, err)
	}
}

// Trash then Purge end to end with the clock advanced past the age.
func TestTrashThenPurge_EndToEnd(t *testing.T) {
	e := newEnv(t)
	now := testNow
	e.cfg.Now = func() time.Time { return now }
	tr := e.trasher()
	src := e.write("f.txt", "x")
	if err := tr.Trash(src); err != nil {
		t.Fatal(err)
	}
	now = testNow.Add(29 * 24 * time.Hour)
	if n, _ := tr.Purge(); n != 0 || !exists(filepath.Join(e.home, "files", "f.txt")) {
		t.Fatalf("purged too early (n=%d)", n)
	}
	now = testNow.Add(31 * 24 * time.Hour)
	if n, err := tr.Purge(); n != 1 || err != nil || exists(filepath.Join(e.home, "files", "f.txt")) {
		t.Fatalf("not purged after the age: n=%d err=%v", n, err)
	}
}
