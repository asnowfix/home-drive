package syncer

import "os"

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
// remover (unit tests that do not exercise the trash; the agent always
// wires one) falls back to os.Remove.
func removeLocalPath(r LocalRemover, path string) error {
	if r == nil {
		return os.Remove(path)
	}
	return r.Remove(path)
}
