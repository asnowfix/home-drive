// Package trash moves local files to the freedesktop.org Trash instead of
// deleting them (issue #155), and purges the entries homedrive trashed itself
// once they are old enough. Spec:
// https://specifications.freedesktop.org/trash-spec/latest/
//
// Placement: the home trash ($XDG_DATA_HOME/Trash) by plain rename; when the
// rename fails with EXDEV or EROFS (the file is on another mount, or the home
// trash is read-only, as under the packaged system unit's sandbox), the
// topdir trash of the file's mount: $topdir/.Trash/$uid if an admin made a
// valid $topdir/.Trash, else $topdir/.Trash-$uid. The topdir comes from
// mountinfo. If that fails too the file is left in place: never a copy, never
// a hard delete.
package trash

import (
	"errors"
	"fmt"
	"io/fs"
	"log/slog"
	"net/url"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"time"

	"github.com/asnowfix/home-drive/homedrive/internal/store"
)

// ErrUnavailable is returned when neither the home trash nor the topdir
// trash could take the file. The file is untouched.
var ErrUnavailable = errors.New("trash: no usable trash directory")

// dateLayout is the trashinfo DeletionDate format (local time, no zone).
const dateLayout = "2006-01-02T15:04:05"

// maxNameTries bounds the collision search for a free entry name.
const maxNameTries = 10000

// Recorder persists which entries homedrive trashed. *store.Journal
// implements it.
type Recorder interface {
	PutTrashRecord(r store.TrashRecord) error
	DeleteTrashRecord(dir, name string) error
	ListTrashRecords() ([]store.TrashRecord, error)
}

// Config configures a Trasher. Only Records and Log are required; the rest
// has production defaults and is injectable for tests.
type Config struct {
	HomeTrash string        // home trash dir; default $XDG_DATA_HOME/Trash
	UID       int           // default os.Getuid()
	PurgeAge  time.Duration // purge own entries older than this; <= 0 disables
	DryRun    bool          // trash and purge nothing, audit only
	LocalRoot string        // sync root; a topdir trash below it is refused

	Rename    func(oldpath, newpath string) error // default os.Rename
	Mountinfo func() ([]byte, error)              // default /proc/self/mountinfo
	Now       func() time.Time                    // default time.Now

	Records Recorder
	Audit   *store.Auditor // optional
	Log     *slog.Logger
}

// Trasher trashes local files and purges its own entries.
type Trasher struct{ cfg Config }

// New returns a Trasher, filling in the defaults.
func New(cfg Config) *Trasher {
	if cfg.HomeTrash == "" {
		cfg.HomeTrash = DefaultHomeTrash()
	}
	if cfg.UID == 0 {
		cfg.UID = os.Getuid()
	}
	if cfg.Rename == nil {
		cfg.Rename = os.Rename
	}
	if cfg.Mountinfo == nil {
		cfg.Mountinfo = readProcMountinfo
	}
	if cfg.Now == nil {
		cfg.Now = time.Now
	}
	if cfg.Log == nil {
		cfg.Log = slog.Default()
	}
	return &Trasher{cfg: cfg}
}

// DefaultHomeTrash is $XDG_DATA_HOME/Trash, ~/.local/share/Trash when unset.
func DefaultHomeTrash() string {
	if x := os.Getenv("XDG_DATA_HOME"); filepath.IsAbs(x) {
		return filepath.Join(x, "Trash")
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return ""
	}
	return filepath.Join(home, ".local", "share", "Trash")
}

// Remove deletes one path the way sync's local deletions do: an empty
// directory is removed (nothing in it to recover; os.Remove refuses a
// non-empty one), anything else is trashed.
func (t *Trasher) Remove(path string) error {
	if t == nil {
		return fmt.Errorf("%w: no trasher configured", ErrUnavailable)
	}
	info, err := os.Lstat(path)
	if err != nil {
		return err
	}
	if info.IsDir() {
		if t.cfg.DryRun {
			return nil
		}
		return os.Remove(path)
	}
	return t.Trash(path)
}

// Trash moves path (a file or symlink) to the trash. On failure the file is
// left in place, the failure is logged at Error and audited, and the error
// wraps ErrUnavailable.
func (t *Trasher) Trash(path string) error {
	if t == nil {
		return fmt.Errorf("%w: no trasher configured", ErrUnavailable)
	}
	target, err := resolve(path)
	if err != nil {
		return err
	}
	if info, err := os.Lstat(target); err == nil && info.IsDir() {
		return fmt.Errorf("trash: %s is a directory, only files are trashed", target)
	}
	if t.cfg.DryRun {
		t.audit(store.AuditEntry{Op: "trash", Path: target, DryRun: true})
		return nil
	}
	homeErr := fmt.Errorf("no home trash directory: %w", syscall.EROFS)
	if t.cfg.HomeTrash != "" {
		_, homeErr = t.trashInto(t.cfg.HomeTrash, target, target)
	}
	if homeErr == nil {
		return nil
	}
	if !errors.Is(homeErr, syscall.EXDEV) && !errors.Is(homeErr, syscall.EROFS) {
		return t.fail(target, homeErr)
	}
	cands, topErr := t.topdirTrashes(target)
	if topErr != nil {
		return t.fail(target, errors.Join(homeErr, topErr))
	}
	errs := []error{homeErr}
	for _, c := range cands {
		_, err := t.trashInto(c.dir, target, c.pathField(target))
		if err == nil {
			return nil
		}
		errs = append(errs, err)
	}
	return t.fail(target, errors.Join(errs...))
}

// fail logs, audits and wraps a trashing failure. Nothing was moved.
func (t *Trasher) fail(target string, err error) error {
	t.cfg.Log.Error("cannot trash local file: leaving it in place",
		"path", target, "op", "trash", "error", err)
	t.audit(store.AuditEntry{Op: "trash", Path: target, Error: err.Error()})
	return fmt.Errorf("%w: %s: %w", ErrUnavailable, target, err)
}

// resolve returns path with its parent directory's symlinks resolved (the
// file itself is not followed), so it matches the mountinfo paths.
func resolve(path string) (string, error) {
	abs, err := filepath.Abs(path)
	if err != nil {
		return "", fmt.Errorf("trash: absolute path of %s: %w", path, err)
	}
	if _, err := os.Lstat(abs); err != nil {
		return "", err
	}
	dir, err := filepath.EvalSymlinks(filepath.Dir(abs))
	if err != nil {
		return "", fmt.Errorf("trash: resolving %s: %w", filepath.Dir(abs), err)
	}
	return filepath.Join(dir, filepath.Base(abs)), nil
}

// topTrash is a topdir trash directory.
type topTrash struct{ dir, topdir string }

// pathField is the .trashinfo Path value: relative to the topdir.
func (tt topTrash) pathField(target string) string {
	rel, err := filepath.Rel(tt.topdir, target)
	if err != nil {
		return target
	}
	return rel
}

// topdirTrashes returns the topdir trash candidates for target's mount, in
// order: a valid admin $topdir/.Trash/$uid, then $topdir/.Trash-$uid. It
// refuses a topdir strictly below the sync root: that trash would sit inside
// the synced tree where only a root-level .Trash* is excluded.
func (t *Trasher) topdirTrashes(target string) ([]topTrash, error) {
	mi, err := t.cfg.Mountinfo()
	if err != nil {
		return nil, fmt.Errorf("reading mount table: %w", err)
	}
	top, err := topdirOf(mi, target)
	if err != nil {
		return nil, err
	}
	if t.cfg.LocalRoot != "" {
		root, rerr := filepath.EvalSymlinks(t.cfg.LocalRoot)
		if rerr != nil {
			return nil, fmt.Errorf("cannot resolve the sync root, refusing a topdir trash: %w", rerr)
		}
		if top != root && underMount(root, top) {
			return nil, fmt.Errorf("mount %s is nested inside the sync root %s: its trash would be synced", top, root)
		}
	}
	uid := strconv.Itoa(t.cfg.UID)
	var out []topTrash
	admin := filepath.Join(top, ".Trash")
	if info, err := os.Lstat(admin); err == nil && info.IsDir() &&
		info.Mode()&os.ModeSymlink == 0 && info.Mode()&os.ModeSticky != 0 {
		out = append(out, topTrash{dir: filepath.Join(admin, uid), topdir: top})
	}
	return append(out, topTrash{dir: filepath.Join(top, ".Trash-"+uid), topdir: top}), nil
}

// plainDir checks that path is a real directory (not a symlink), creating it
// with mode 0700 when create is set and it is missing. Trash directories can
// live in a user-writable tree, so symlinks are never followed.
func plainDir(path string, create bool) error {
	fi, err := os.Lstat(path)
	switch {
	case err == nil:
		if fi.Mode()&os.ModeSymlink != 0 || !fi.IsDir() {
			return fmt.Errorf("trash: %s is not a plain directory", path)
		}
		return nil
	case create && errors.Is(err, fs.ErrNotExist):
		return os.Mkdir(path, 0o700)
	}
	return err
}

// trashInto moves target into the trash directory dir under a free name:
// the .trashinfo is created first with O_EXCL (reserving the name, as the
// spec asks), then the file renamed into files/. Any failure removes the
// reservation and leaves target where it was.
func (t *Trasher) trashInto(dir, target, pathField string) (string, error) {
	if err := os.MkdirAll(filepath.Dir(dir), 0o755); err != nil {
		return "", err
	}
	for _, sub := range []string{"", "files", "info"} {
		if err := plainDir(filepath.Join(dir, sub), true); err != nil {
			return "", err
		}
	}
	now := t.cfg.Now().Local().Truncate(time.Second)
	date := now.Format(dateLayout)
	body := "[Trash Info]\nPath=" + encodePath(pathField) + "\nDeletionDate=" + date + "\n"
	base := filepath.Base(target)
	for i := 0; i < maxNameTries; i++ {
		name := base
		if i > 0 {
			name = base + "." + strconv.Itoa(i+1)
		}
		infoPath := filepath.Join(dir, "info", name+".trashinfo")
		filePath := filepath.Join(dir, "files", name)
		if _, err := os.Lstat(filePath); err == nil {
			continue
		}
		f, err := os.OpenFile(infoPath, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o600)
		if errors.Is(err, fs.ErrExist) {
			continue
		}
		if err != nil {
			return "", err
		}
		_, werr := f.WriteString(body)
		if cerr := f.Close(); werr == nil {
			werr = cerr
		}
		if werr == nil {
			werr = t.cfg.Rename(target, filePath)
		}
		if werr != nil {
			_ = os.Remove(infoPath)
			return "", werr
		}
		t.recordTrashed(dir, name, target, filePath, now, date)
		return filePath, nil
	}
	return "", fmt.Errorf("no free name for %s in %s", base, dir)
}

// recordTrashed remembers the entry (so it can be purged later) and audits.
func (t *Trasher) recordTrashed(dir, name, orig, filePath string, at time.Time, date string) {
	if t.cfg.Records != nil {
		rec := store.TrashRecord{Dir: dir, Name: name, Path: orig, DeletedAt: at.UTC(), Date: date}
		if err := t.cfg.Records.PutTrashRecord(rec); err != nil {
			t.cfg.Log.Error("trashed file but could not record it: it will not be auto-purged",
				"path", orig, "op", "trash", "error", err)
		}
	}
	t.cfg.Log.Info("moved local file to trash",
		"path", orig, "op", "trash", "trash_dir", dir, "entry", name)
	t.audit(store.AuditEntry{Op: "trash", Path: orig, NewPath: filePath})
}

func (t *Trasher) audit(e store.AuditEntry) {
	if t.cfg.Audit != nil {
		t.cfg.Audit.Log(e)
	}
}

// encodePath percent-encodes p per the spec (RFC 3986), keeping "/".
func encodePath(p string) string {
	parts := strings.Split(p, "/")
	for i, s := range parts {
		parts[i] = url.PathEscape(s)
	}
	return strings.Join(parts, "/")
}
