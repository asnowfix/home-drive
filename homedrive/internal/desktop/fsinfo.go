package desktop

import (
	"fmt"
	"syscall"
)

// FilesystemSize returns the total size in bytes of the filesystem holding
// path.
func FilesystemSize(path string) (int64, error) {
	var st syscall.Statfs_t
	if err := syscall.Statfs(path, &st); err != nil {
		return 0, fmt.Errorf("statfs %q: %w", path, err)
	}
	return int64(st.Blocks) * int64(st.Bsize), nil //nolint:gosec,unconvert // filesystem sizes fit in int64; field widths differ per OS
}
