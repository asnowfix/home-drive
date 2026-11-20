package trash

import (
	"bufio"
	"errors"
	"fmt"
	"io/fs"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/asnowfix/home-drive/homedrive/internal/store"
)

// Purge removes the entries homedrive trashed itself (those in the state
// record) whose deletion is older than the configured age. An entry is only
// touched while its .trashinfo still matches the record (same DeletionDate
// string and original path): if it was restored, emptied by the user, or
// replaced by a foreign entry of the same name, the record is dropped (and
// logged) and the files are left alone. Entries not in the record are never
// read. The trash directories are never followed through symlinks, and only
// non-directory entries are removed. Dry-run audits what would be purged and
// changes nothing. It returns the number of entries purged (or that would
// be).
func (t *Trasher) Purge() (int, error) {
	if t == nil || t.cfg.PurgeAge <= 0 || t.cfg.Records == nil {
		return 0, nil
	}
	recs, err := t.cfg.Records.ListTrashRecords()
	if err != nil {
		return 0, fmt.Errorf("trash: listing records: %w", err)
	}
	cutoff := t.cfg.Now().Add(-t.cfg.PurgeAge)
	var errs []error
	n := 0
	for _, r := range recs {
		if !r.DeletedAt.Before(cutoff) {
			continue
		}
		purged, err := t.purgeOne(r)
		if err != nil {
			t.cfg.Log.Error("cannot purge trash entry", "op", "trash_purge",
				"path", r.Path, "trash_dir", r.Dir, "entry", r.Name, "error", err)
			errs = append(errs, err)
			continue
		}
		if purged {
			n++
		}
	}
	return n, errors.Join(errs...)
}

// dropRecord forgets a record whose entry is no longer ours.
func (t *Trasher) dropRecord(r store.TrashRecord, why string) error {
	t.cfg.Log.Info("dropping trash record: entry is no longer ours, leaving it alone",
		"op", "trash_purge", "path", r.Path, "trash_dir", r.Dir, "entry", r.Name, "reason", why)
	if t.cfg.DryRun {
		return nil
	}
	return t.cfg.Records.DeleteTrashRecord(r.Dir, r.Name)
}

func (t *Trasher) purgeOne(r store.TrashRecord) (bool, error) {
	if !filepath.IsAbs(r.Dir) || r.Name == "" || r.Name != filepath.Base(r.Name) ||
		r.Name == "." || r.Name == ".." {
		return false, fmt.Errorf("trash: refusing malformed record %q/%q", r.Dir, r.Name)
	}
	for _, d := range []string{r.Dir, filepath.Join(r.Dir, "files"), filepath.Join(r.Dir, "info")} {
		if err := plainDir(d, false); err != nil {
			if errors.Is(err, fs.ErrNotExist) {
				return false, t.dropRecord(r, "trash directory is gone")
			}
			return false, err
		}
	}
	info := filepath.Join(r.Dir, "info", r.Name+".trashinfo")
	date, origPath, err := readTrashInfo(info)
	switch {
	case errors.Is(err, fs.ErrNotExist):
		return false, t.dropRecord(r, "trashinfo is gone (restored or emptied)")
	case err != nil:
		return false, err
	case !matches(r, date, origPath):
		return false, t.dropRecord(r, "trashinfo no longer matches the record")
	}
	entry := filepath.Join(r.Dir, "files", r.Name)
	if fi, err := os.Lstat(entry); err == nil && fi.IsDir() {
		return false, fmt.Errorf("trash: %s is a directory, but homedrive only trashes files", entry)
	}
	if t.cfg.DryRun {
		t.audit(store.AuditEntry{Op: "trash_purge", Path: r.Path, DryRun: true})
		return true, nil
	}
	if err := os.Remove(entry); err != nil && !errors.Is(err, fs.ErrNotExist) {
		return false, err
	}
	if err := os.Remove(info); err != nil && !errors.Is(err, fs.ErrNotExist) {
		return false, err
	}
	if err := t.cfg.Records.DeleteTrashRecord(r.Dir, r.Name); err != nil {
		return true, err
	}
	t.cfg.Log.Info("purged trashed local file", "path", r.Path, "op", "trash_purge",
		"trash_dir", r.Dir, "entry", r.Name)
	t.audit(store.AuditEntry{Op: "trash_purge", Path: r.Path})
	return true, nil
}

// matches reports whether a .trashinfo still describes record r. The Path
// key is absolute in the home trash and relative to the topdir otherwise,
// so a relative one is compared against the tail of the recorded path.
func matches(r store.TrashRecord, date, origPath string) bool {
	want := r.Date
	if want == "" { // record written before Date existed
		want = r.DeletedAt.Local().Format(dateLayout)
	}
	if date != want {
		return false
	}
	return origPath == r.Path || (!filepath.IsAbs(origPath) &&
		strings.HasSuffix(r.Path, string(filepath.Separator)+filepath.FromSlash(origPath)))
}

// readTrashInfo returns the DeletionDate string (validated) and the decoded
// Path of a .trashinfo file.
func readTrashInfo(path string) (string, string, error) {
	f, err := os.Open(path)
	if err != nil {
		return "", "", err
	}
	defer func() { _ = f.Close() }()
	var date, origPath string
	sc := bufio.NewScanner(f)
	for sc.Scan() {
		k, v, ok := strings.Cut(sc.Text(), "=")
		switch {
		case ok && k == "DeletionDate":
			if _, err := time.Parse(dateLayout, v); err != nil {
				return "", "", fmt.Errorf("trash: bad DeletionDate in %s: %w", path, err)
			}
			date = v
		case ok && k == "Path":
			if origPath, err = url.PathUnescape(v); err != nil {
				return "", "", fmt.Errorf("trash: bad Path in %s: %w", path, err)
			}
		}
	}
	if err := sc.Err(); err != nil {
		return "", "", err
	}
	if date == "" {
		return "", "", fmt.Errorf("trash: no DeletionDate in %s", path)
	}
	return date, origPath, nil
}
