package trash

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"syscall"
	"testing"
	"time"

	"github.com/asnowfix/home-drive/homedrive/internal/store"
)

// Review M2: the trash directory sits inside the user-writable synced tree
// under the sandbox, so symlinks planted in it must never be followed.

func TestTrash_RefusesSymlinkedTrashDirs(t *testing.T) {
	tests := []struct {
		name  string
		plant func(e *env, victim string)
	}{
		{"topdir_trash_is_symlink", func(e *env, victim string) {
			_ = os.Symlink(victim, filepath.Join(e.root, ".Trash-1000"))
		}},
		{"files_subdir_is_symlink", func(e *env, victim string) {
			d := filepath.Join(e.root, ".Trash-1000")
			_ = os.MkdirAll(filepath.Join(d, "info"), 0o700)
			_ = os.Symlink(victim, filepath.Join(d, "files"))
		}},
		{"info_subdir_is_symlink", func(e *env, victim string) {
			d := filepath.Join(e.root, ".Trash-1000")
			_ = os.MkdirAll(filepath.Join(d, "files"), 0o700)
			_ = os.Symlink(victim, filepath.Join(d, "info"))
		}},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			e := newEnv(t)
			e.cfg.Rename = failUnder(e.home, syscall.EXDEV)
			victim := t.TempDir()
			tc.plant(e, victim)
			src := e.write("f.txt", "keep")

			err := e.trasher().Trash(src)
			if !errors.Is(err, ErrUnavailable) || !exists(src) {
				t.Fatalf("err = %v, source kept = %v; want refusal", err, exists(src))
			}
			if es, _ := os.ReadDir(victim); len(es) != 0 {
				t.Errorf("wrote through a symlink into %s: %v", victim, es)
			}
		})
	}
}

func TestPurge_NeverFollowsSymlinksOrRemovesDirs(t *testing.T) {
	old := testNow.Add(-90 * 24 * time.Hour)
	tests := []struct {
		name  string
		plant func(t *testing.T, e *env, dir, victim string)
	}{
		{"files_replaced_by_symlink", func(t *testing.T, e *env, dir, victim string) {
			plantEntry(t, dir, "f", e.root+"/f", old)
			_ = os.Remove(filepath.Join(dir, "files", "f"))
			_ = os.Remove(filepath.Join(dir, "files"))
			_ = os.Symlink(victim, filepath.Join(dir, "files"))
			// the victim holds a same-named entry that must survive
			_ = os.WriteFile(filepath.Join(victim, "f"), []byte("v"), 0o600)
		}},
		{"info_replaced_by_symlink", func(t *testing.T, e *env, dir, victim string) {
			plantEntry(t, dir, "f", e.root+"/f", old)
			_ = os.WriteFile(filepath.Join(victim, "f.trashinfo"),
				[]byte("[Trash Info]\nPath="+e.root+"/f\nDeletionDate="+old.Format(dateLayout)+"\n"), 0o600)
			_ = os.RemoveAll(filepath.Join(dir, "info"))
			_ = os.Symlink(victim, filepath.Join(dir, "info"))
			_ = os.WriteFile(filepath.Join(victim, "f"), []byte("v"), 0o600)
		}},
		{"entry_is_a_directory", func(t *testing.T, e *env, dir, victim string) {
			plantEntry(t, dir, "f", e.root+"/f", old)
			_ = os.Remove(filepath.Join(dir, "files", "f"))
			_ = os.MkdirAll(filepath.Join(dir, "files", "f"), 0o700)
			_ = os.WriteFile(filepath.Join(dir, "files", "f", "inner"), []byte("v"), 0o600)
		}},
		{"trash_dir_itself_is_symlink", func(t *testing.T, e *env, dir, victim string) {
			real := filepath.Join(victim, "real")
			plantEntry(t, real, "f", e.root+"/f", old)
			_ = os.Symlink(real, dir)
		}},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			e := newEnv(t)
			victim := t.TempDir()
			dir := filepath.Join(e.root, ".Trash-1000")
			tc.plant(t, e, dir, victim)
			_ = e.recs.PutTrashRecord(store.TrashRecord{Dir: dir, Name: "f", Path: e.root + "/f",
				DeletedAt: old, Date: old.Format(dateLayout)})

			n, err := e.trasher().Purge()
			if err == nil || n != 0 {
				t.Errorf("Purge = %d, %v; want a refusal error", n, err)
			}
			var survivors []string
			_ = filepath.WalkDir(victim, func(p string, d os.DirEntry, _ error) error {
				if !d.IsDir() {
					survivors = append(survivors, p)
				}
				return nil
			})
			if tc.name != "entry_is_a_directory" && len(survivors) == 0 {
				t.Errorf("victim tree was emptied")
			}
			if tc.name == "entry_is_a_directory" && !exists(filepath.Join(dir, "files", "f", "inner")) {
				t.Error("directory entry was removed")
			}
			if recs, _ := e.recs.ListTrashRecords(); len(recs) != 1 {
				t.Errorf("record dropped on a refusal: %+v", recs)
			}
		})
	}
}

// Review S1: a mount nested inside local_root would put its trash inside the
// synced tree, unexcluded (only the root-level name is). Refuse it.
func TestTrash_RefusesTopdirInsideLocalRoot(t *testing.T) {
	e := newEnv(t)
	nested := filepath.Join(e.root, "photos")
	e.cfg.Mountinfo = func() ([]byte, error) {
		return []byte("1 0 8:1 / / rw - ext4 /dev/a rw\n2 1 8:1 /d " + e.root + " rw - ext4 /dev/a rw\n" +
			"3 2 8:2 / " + nested + " rw - ext4 /dev/b rw\n"), nil
	}
	e.cfg.Rename = failUnder(e.home, syscall.EXDEV)
	src := e.write("photos/p.jpg", "img")

	err := e.trasher().Trash(src)
	if !errors.Is(err, ErrUnavailable) || !exists(src) {
		t.Fatalf("err = %v, kept = %v; want refusal", err, exists(src))
	}
	if exists(filepath.Join(nested, ".Trash-1000")) {
		t.Error("a trash was created inside the synced tree below local_root")
	}
	if l := e.auditLines(); len(l) != 1 || l[0].Error == "" {
		t.Errorf("audit = %+v, want one failed trash line", l)
	}
}

// Review S3: Trash itself refuses directories.
func TestTrash_RefusesDirectory(t *testing.T) {
	e := newEnv(t)
	e.write("d/f", "x")
	d := filepath.Join(e.root, "d")
	if err := e.trasher().Trash(d); err == nil {
		t.Fatal("Trash(dir) succeeded")
	}
	if !exists(filepath.Join(d, "f")) {
		t.Error("directory was moved")
	}
}

// Nit: an unusable admin .Trash/<uid> falls back to .Trash-<uid>.
func TestTrash_UnusableAdminTrashFallsBack(t *testing.T) {
	e := newEnv(t)
	e.cfg.Rename = failUnder(e.home, syscall.EXDEV)
	mkAdminTrash(t, e.root, true)
	// .Trash/1000 exists as a plain file: unusable.
	if err := os.WriteFile(filepath.Join(e.root, ".Trash", "1000"), nil, 0o600); err != nil {
		t.Fatal(err)
	}
	src := e.write("f.txt", "x")
	if err := e.trasher().Trash(src); err != nil {
		t.Fatalf("Trash: %v", err)
	}
	if !exists(filepath.Join(e.root, ".Trash-1000", "files", "f.txt")) {
		t.Error("did not fall back to .Trash-<uid>")
	}
}

// Review S4: DeletionDate is compared as written, so a timezone change
// between trashing and purging does not orphan the entry; dropping a record
// is logged.
func TestPurge_TimezoneChangeStillPurges(t *testing.T) {
	e := newEnv(t)
	now := testNow
	e.cfg.Now = func() time.Time { return now }
	tr := e.trasher()
	if err := tr.Trash(e.write("f.txt", "x")); err != nil {
		t.Fatal(err)
	}
	saved := time.Local
	t.Cleanup(func() { time.Local = saved })
	time.Local = time.FixedZone("shifted", 5*3600)
	now = testNow.Add(40 * 24 * time.Hour)

	n, err := tr.Purge()
	if err != nil || n != 1 || exists(filepath.Join(e.home, "files", "f.txt")) {
		t.Fatalf("Purge = %d, %v; entry should have been purged", n, err)
	}
}

func TestPurge_DroppedRecordIsLogged(t *testing.T) {
	e := newEnv(t)
	old := testNow.Add(-90 * 24 * time.Hour)
	_ = e.recs.PutTrashRecord(store.TrashRecord{Dir: e.home, Name: "gone", Path: "/x",
		DeletedAt: old, Date: old.Format(dateLayout)})
	if _, err := e.trasher().Purge(); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(e.logs.String(), "dropping trash record") {
		t.Errorf("no log for the dropped record: %q", e.logs.String())
	}
}

func TestRemove_DryRunLeavesEmptyDir(t *testing.T) {
	e := newEnv(t)
	e.cfg.DryRun = true
	d := filepath.Join(e.root, "d")
	_ = os.Mkdir(d, 0o755)
	if err := e.trasher().Remove(d); err != nil || !exists(d) {
		t.Fatalf("Remove in dry-run: err=%v, dir kept=%v", err, exists(d))
	}
}

// A typed-nil *Trasher stored in an interface must not panic or delete.
func TestNilTrasher_IsSafe(t *testing.T) {
	var tr *Trasher
	if err := tr.Remove("/nonexistent"); !errors.Is(err, ErrUnavailable) {
		t.Errorf("Remove on nil trasher = %v, want ErrUnavailable", err)
	}
	if n, err := tr.Purge(); n != 0 || err != nil {
		t.Errorf("Purge on nil trasher = %d, %v", n, err)
	}
}
