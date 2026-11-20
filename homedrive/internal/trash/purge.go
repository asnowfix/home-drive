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
// record) whose DeletionDate is older than the configured age. An entry is
// only touched while its .trashinfo still matches the record (same
// DeletionDate and original path): if it was restored, emptied by the user,
// or replaced by a foreign entry of the same name, the record is dropped and
// the files are left alone. Entries not in the record are never read.
// Dry-run audits what would be purged and changes nothing. It returns the
// number of entries purged (or that would be).
func (t *Trasher) Purge() (int, error) {
	if t.cfg.PurgeAge <= 0 || t.cfg.Records == nil {
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
			t.cfg.Log.Warn("cannot purge trash entry", "op", "trash_purge",
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

func (t *Trasher) purgeOne(r store.TrashRecord) (bool, error) {
	if !filepath.IsAbs(r.Dir) || r.Name == "" || r.Name != filepath.Base(r.Name) ||
		r.Name == "." || r.Name == ".." {
		return false, fmt.Errorf("trash: refusing malformed record %q/%q", r.Dir, r.Name)
	}
	info := filepath.Join(r.Dir, "info", r.Name+".trashinfo")
	date, origPath, err := readTrashInfo(info)
	switch {
	case errors.Is(err, fs.ErrNotExist) || (err == nil && !matches(r, date, origPath)):
		// Restored, emptied, or replaced by someone else: not ours any more.
		if t.cfg.DryRun {
			return false, nil
		}
		return false, t.cfg.Records.DeleteTrashRecord(r.Dir, r.Name)
	case err != nil:
		return false, err
	}
	if t.cfg.DryRun {
		t.audit(store.AuditEntry{Op: "trash_purge", Path: r.Path, DryRun: true})
		return true, nil
	}
	if err := os.RemoveAll(filepath.Join(r.Dir, "files", r.Name)); err != nil {
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
func matches(r store.TrashRecord, date time.Time, origPath string) bool {
	if !date.Equal(r.DeletedAt) {
		return false
	}
	return origPath == r.Path || (!filepath.IsAbs(origPath) &&
		strings.HasSuffix(r.Path, string(filepath.Separator)+filepath.FromSlash(origPath)))
}

// readTrashInfo parses DeletionDate and Path out of a .trashinfo file.
func readTrashInfo(path string) (time.Time, string, error) {
	f, err := os.Open(path)
	if err != nil {
		return time.Time{}, "", err
	}
	defer func() { _ = f.Close() }()
	var (
		date     time.Time
		origPath string
		hasDate  bool
	)
	sc := bufio.NewScanner(f)
	for sc.Scan() {
		k, v, ok := strings.Cut(sc.Text(), "=")
		switch {
		case ok && k == "DeletionDate":
			date, err = time.ParseInLocation(dateLayout, v, time.Local)
			if err != nil {
				return time.Time{}, "", fmt.Errorf("trash: bad DeletionDate in %s: %w", path, err)
			}
			hasDate = true
		case ok && k == "Path":
			if origPath, err = url.PathUnescape(v); err != nil {
				return time.Time{}, "", fmt.Errorf("trash: bad Path in %s: %w", path, err)
			}
		}
	}
	if err := sc.Err(); err != nil {
		return time.Time{}, "", err
	}
	if !hasDate {
		return time.Time{}, "", fmt.Errorf("trash: no DeletionDate in %s", path)
	}
	return date, origPath, nil
}
