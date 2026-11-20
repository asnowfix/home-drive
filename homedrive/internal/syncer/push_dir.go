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

// unpairedRenameAsRemove turns an unpaired OpRename (the old path of a file
// moved out of the watched tree, e.g. into the trash, or a user's mv out of
// local_root) into the OpRemove it amounts to. If the path exists again the
// event is stale and skipped. Other events pass through. Issue #155.
func (s *Syncer) unpairedRenameAsRemove(ev Event) (Event, bool) {
	if ev.Op != OpRename {
		return ev, false
	}
	if _, err := os.Lstat(filepath.Join(s.cfg.LocalRoot, filepath.FromSlash(ev.Path))); err == nil {
		return ev, true
	}
	ev.Op = OpRemove
	return ev, false
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
