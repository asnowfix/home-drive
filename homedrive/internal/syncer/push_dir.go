package syncer

import (
	"log/slog"
	"os"
	"path/filepath"

	"github.com/asnowfix/home-drive/homedrive/internal/pathfilter"
)

// skipTrashEvent reports whether ev concerns a topdir trash directory
// directly under the local root (".Trash-<uid>", ".Trash"), which is never
// pushed (issue #155). The watcher already excludes it; this keeps the push
// path safe on its own.
func (s *Syncer) skipTrashEvent(ev Event) bool {
	return pathfilter.IsTrash(filepath.ToSlash(ev.Path))
}

// skipDirEvent reports whether ev is a create/write event for a local
// directory, which must not be uploaded as a file (issue #105). Directories
// are not synced on their own: Drive folders are created implicitly when a
// file beneath them is uploaded, so an empty local directory has no remote
// counterpart. Remove events are never skipped (the path no longer exists to
// stat) and a stat failure falls through to the normal upload path.
func (s *Syncer) skipDirEvent(logger *slog.Logger, ev Event) bool {
	if ev.Op != OpCreate && ev.Op != OpWrite {
		return false
	}
	info, err := os.Stat(filepath.Join(s.cfg.LocalRoot, filepath.FromSlash(ev.Path)))
	if err != nil || !info.IsDir() {
		return false
	}
	logger.Debug("skipping directory event: directories are not pushed on their own",
		"origin", "local")
	return true
}
