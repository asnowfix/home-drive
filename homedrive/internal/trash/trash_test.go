package trash

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"syscall"
	"testing"
)

func TestTrash_Placement(t *testing.T) {
	tests := []struct {
		name string
		// prepare may adjust cfg and create admin trash dirs under e.root.
		prepare  func(e *env)
		wantDir  func(e *env) string // trash dir expected to hold the file
		wantPath func(e *env) string // expected Path= value (decoded)
	}{
		{
			name:     "home_trash_same_filesystem",
			prepare:  func(*env) {},
			wantDir:  func(e *env) string { return e.home },
			wantPath: func(e *env) string { return filepath.Join(e.root, "d", "f.txt") },
		},
		{
			name:     "exdev_falls_back_to_topdir_dot_trash_uid",
			prepare:  func(e *env) { e.cfg.Rename = failUnder(e.home, syscall.EXDEV) },
			wantDir:  func(e *env) string { return filepath.Join(e.root, ".Trash-1000") },
			wantPath: func(*env) string { return "d/f.txt" },
		},
		{
			name:     "erofs_falls_back_to_topdir",
			prepare:  func(e *env) { e.cfg.Rename = failUnder(e.home, syscall.EROFS) },
			wantDir:  func(e *env) string { return filepath.Join(e.root, ".Trash-1000") },
			wantPath: func(*env) string { return "d/f.txt" },
		},
		{
			name: "valid_admin_trash_is_used",
			prepare: func(e *env) {
				e.cfg.Rename = failUnder(e.home, syscall.EXDEV)
				mkAdminTrash(t, e.root, true)
			},
			wantDir:  func(e *env) string { return filepath.Join(e.root, ".Trash", "1000") },
			wantPath: func(*env) string { return "d/f.txt" },
		},
		{
			name: "admin_trash_without_sticky_bit_is_ignored",
			prepare: func(e *env) {
				e.cfg.Rename = failUnder(e.home, syscall.EXDEV)
				mkAdminTrash(t, e.root, false)
			},
			wantDir:  func(e *env) string { return filepath.Join(e.root, ".Trash-1000") },
			wantPath: func(*env) string { return "d/f.txt" },
		},
		{
			name: "admin_trash_symlink_is_ignored",
			prepare: func(e *env) {
				e.cfg.Rename = failUnder(e.home, syscall.EXDEV)
				if err := os.Symlink(t.TempDir(), filepath.Join(e.root, ".Trash")); err != nil {
					t.Fatal(err)
				}
			},
			wantDir:  func(e *env) string { return filepath.Join(e.root, ".Trash-1000") },
			wantPath: func(*env) string { return "d/f.txt" },
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			e := newEnv(t)
			tc.prepare(e)
			src := e.write("d/f.txt", "payload")

			if err := e.trasher().Trash(src); err != nil {
				t.Fatalf("Trash: %v", err)
			}

			dir := tc.wantDir(e)
			if exists(src) {
				t.Error("source still exists")
			}
			if got := readFile(t, filepath.Join(dir, "files", "f.txt")); got != "payload" {
				t.Errorf("trashed content = %q", got)
			}
			wantInfo := "[Trash Info]\nPath=" + tc.wantPath(e) +
				"\nDeletionDate=2026-11-20T12:00:00\n"
			if got := readFile(t, filepath.Join(dir, "info", "f.txt.trashinfo")); got != wantInfo {
				t.Errorf("trashinfo = %q, want %q", got, wantInfo)
			}
			if strings.Contains(dir, ".Trash-") {
				if fi, err := os.Stat(dir); err != nil || fi.Mode().Perm() != 0o700 {
					t.Errorf("topdir trash mode = %v (err %v), want 0700", fi.Mode().Perm(), err)
				}
			}
			recs, _ := e.recs.ListTrashRecords()
			if len(recs) != 1 || recs[0].Dir != dir || recs[0].Name != "f.txt" ||
				!recs[0].DeletedAt.Equal(testNow) {
				t.Errorf("records = %+v", recs)
			}
			lines := e.auditLines()
			if len(lines) != 1 || lines[0].Op != "trash" || lines[0].Path != src || lines[0].Error != "" {
				t.Errorf("audit = %+v, want one trash line for %s", lines, src)
			}
		})
	}
}

func mkAdminTrash(t *testing.T, root string, sticky bool) {
	t.Helper()
	p := filepath.Join(root, ".Trash")
	if err := os.Mkdir(p, 0o755); err != nil {
		t.Fatal(err)
	}
	mode := os.FileMode(0o777)
	if sticky {
		mode |= os.ModeSticky
	}
	if err := os.Chmod(p, mode); err != nil {
		t.Fatal(err)
	}
}

// Neither trash can take the file: nothing is copied or deleted, the failure
// is audited with an error, and no record or reservation is left behind.
func TestTrash_Failure_LeavesFileInPlace(t *testing.T) {
	tests := []struct {
		name    string
		prepare func(e *env)
	}{
		{"home_exdev_and_topdir_exdev", func(e *env) {
			e.cfg.Rename = func(o, n string) error {
				return &os.LinkError{Op: "rename", Old: o, New: n, Err: syscall.EXDEV}
			}
		}},
		{"home_erofs_and_topdir_erofs", func(e *env) {
			e.cfg.Rename = func(o, n string) error {
				return &os.LinkError{Op: "rename", Old: o, New: n, Err: syscall.EROFS}
			}
		}},
		{"home_exdev_and_no_mount_table", func(e *env) {
			e.cfg.Rename = failUnder(e.home, syscall.EXDEV)
			e.cfg.Mountinfo = func() ([]byte, error) { return nil, errors.New("no /proc") }
		}},
		{"home_exdev_and_topdir_dir_not_creatable", func(e *env) {
			e.cfg.Rename = failUnder(e.home, syscall.EXDEV)
			if err := os.WriteFile(filepath.Join(e.root, ".Trash-1000"), nil, 0o600); err != nil {
				t.Fatal(err)
			}
		}},
		{"unexpected_home_error_does_not_fall_back", func(e *env) {
			e.cfg.Rename = failUnder(e.home, syscall.EACCES)
		}},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			e := newEnv(t)
			tc.prepare(e)
			src := e.write("f.txt", "keep me")

			err := e.trasher().Trash(src)
			if !errors.Is(err, ErrUnavailable) {
				t.Fatalf("Trash error = %v, want ErrUnavailable", err)
			}
			if got := readFile(t, src); got != "keep me" {
				t.Errorf("source content = %q, want it untouched", got)
			}
			for _, d := range []string{e.home, filepath.Join(e.root, ".Trash-1000")} {
				if es, _ := filepath.Glob(filepath.Join(d, "*", "*")); len(es) != 0 {
					t.Errorf("leftover entries in %s: %v", d, es)
				}
			}
			if recs, _ := e.recs.ListTrashRecords(); len(recs) != 0 {
				t.Errorf("records = %+v, want none", recs)
			}
			lines := e.auditLines()
			if len(lines) != 1 || lines[0].Op != "trash" || lines[0].Error == "" {
				t.Errorf("audit = %+v, want one failed trash line", lines)
			}
		})
	}
}

func TestTrash_NameCollisionAndEncoding(t *testing.T) {
	e := newEnv(t)
	tr := e.trasher()
	a := e.write("a/same name.txt", "first")
	b := e.write("b/same name.txt", "second")
	if err := tr.Trash(a); err != nil {
		t.Fatal(err)
	}
	if err := tr.Trash(b); err != nil {
		t.Fatal(err)
	}
	if got := readFile(t, filepath.Join(e.home, "files", "same name.txt")); got != "first" {
		t.Errorf("first = %q", got)
	}
	if got := readFile(t, filepath.Join(e.home, "files", "same name.txt.2")); got != "second" {
		t.Errorf("second = %q", got)
	}
	info := readFile(t, filepath.Join(e.home, "info", "same name.txt.2.trashinfo"))
	if !strings.Contains(info, "Path="+e.root+"/b/same%20name.txt\n") {
		t.Errorf("trashinfo path not percent-encoded: %q", info)
	}
}

func TestTrash_DryRun(t *testing.T) {
	e := newEnv(t)
	e.cfg.DryRun = true
	src := e.write("f.txt", "x")
	if err := e.trasher().Trash(src); err != nil {
		t.Fatal(err)
	}
	if !exists(src) || exists(e.home) {
		t.Error("dry-run must not move the file or create a trash")
	}
	if recs, _ := e.recs.ListTrashRecords(); len(recs) != 0 {
		t.Errorf("records = %+v", recs)
	}
	lines := e.auditLines()
	if len(lines) != 1 || lines[0].Op != "trash" || !lines[0].DryRun {
		t.Errorf("audit = %+v, want one dry-run trash line", lines)
	}
}

func TestRemove_Cases(t *testing.T) {
	tests := []struct {
		name      string
		setup     func(e *env) string
		wantErr   func(error) bool
		wantTrash bool
		wantKept  bool
	}{
		{"file_is_trashed", func(e *env) string { return e.write("f", "x") }, nil, true, false},
		{"empty_dir_is_removed_not_trashed", func(e *env) string {
			p := filepath.Join(e.root, "d")
			_ = os.Mkdir(p, 0o755)
			return p
		}, nil, false, false},
		{"non_empty_dir_refused", func(e *env) string {
			e.write("d/f", "x")
			return filepath.Join(e.root, "d")
		}, func(err error) bool { return err != nil }, false, true},
		{"missing", func(e *env) string { return filepath.Join(e.root, "nope") }, os.IsNotExist, false, false},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			e := newEnv(t)
			p := tc.setup(e)
			err := e.trasher().Remove(p)
			if tc.wantErr == nil && err != nil {
				t.Fatalf("Remove: %v", err)
			}
			if tc.wantErr != nil && !tc.wantErr(err) {
				t.Fatalf("Remove error = %v", err)
			}
			if got := exists(p); got != tc.wantKept {
				t.Errorf("path exists = %v, want %v", got, tc.wantKept)
			}
			if got := exists(filepath.Join(e.home, "files", filepath.Base(p))); got != tc.wantTrash {
				t.Errorf("in trash = %v, want %v", got, tc.wantTrash)
			}
		})
	}
}

func TestTopdirOf_Cases(t *testing.T) {
	const table = "1 0 8:1 / / rw - ext4 /dev/sda1 rw\n" +
		"2 1 8:1 /data /srv/data rw - ext4 /dev/sda1 rw\n" +
		"3 2 8:1 /data/my /srv/data/my\\040files rw - ext4 /dev/sda1 rw\n" +
		"4 1 8:1 /x /srv/data2 rw - ext4 /dev/sda1 rw\n"
	tests := []struct {
		path, want string
		wantErr    bool
	}{
		{"/srv/data/a/b.txt", "/srv/data", false},
		{"/srv/data/my files/x", "/srv/data/my files", false},
		{"/srv/data2/x", "/srv/data2", false}, // not a prefix match of /srv/data
		{"/srv/dataX/x", "/", false},
		{"/home/u/f", "/", false},
	}
	for _, tc := range tests {
		got, err := topdirOf([]byte(table), tc.path)
		if (err != nil) != tc.wantErr || got != tc.want {
			t.Errorf("topdirOf(%q) = %q, %v; want %q", tc.path, got, err, tc.want)
		}
	}
	if _, err := topdirOf([]byte("garbage\n"), "/x"); err == nil {
		t.Error("topdirOf with no mounts: want error")
	}
}
