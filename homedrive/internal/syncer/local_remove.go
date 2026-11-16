package syncer

import "os"

// removeLocalPath is the single place where sync deletes anything under the
// local root: the puller's remote-folder/file deletions and bisync's
// propagation of a Drive deletion all go through it (issue #152). It
// removes one file or one empty directory and returns the os.Remove error
// (a missing path satisfies os.IsNotExist). Keeping it a one-function seam
// lets the deletion policy (e.g. a trash directory instead of a hard
// delete) change in one place.
func removeLocalPath(path string) error {
	return os.Remove(path)
}
