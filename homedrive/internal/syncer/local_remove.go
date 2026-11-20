package syncer

import "errors"

// ErrNoRemover is returned when a local deletion is attempted without a
// configured LocalRemover. Refusing is the safe default: a config built
// without the trash must not silently fall back to a hard delete (#155).
var ErrNoRemover = errors.New("syncer: no local remover configured, refusing to delete")

// LocalRemover performs one local deletion under the sync root. The agent
// wires the freedesktop.org trash implementation (internal/trash, issue
// #155): files are moved to the trash, empty directories are removed, and
// when no trash can take a file it is left in place and an error returned.
type LocalRemover interface {
	// Remove deletes one file or one empty directory. A missing path
	// satisfies os.IsNotExist; a non-empty directory is refused.
	Remove(path string) error
}

// removeLocalPath is the single place where sync deletes anything under the
// local root: the puller's remote-folder/file deletions and bisync's
// propagation of a Drive deletion all go through it (issue #152). A nil
// remover refuses (ErrNoRemover); tests that really delete wire one.
func removeLocalPath(r LocalRemover, path string) error {
	if r == nil {
		return ErrNoRemover
	}
	return r.Remove(path)
}
