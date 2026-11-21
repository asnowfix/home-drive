//go:build !linux

package desktop

import (
	"fmt"
	"syscall"
)

// FilesystemID returns the device number of the filesystem holding path.
// (On Linux the statfs fsid is used, which is stable across reboots.)
func FilesystemID(path string) (string, error) {
	dev, err := statDev(path)
	if err != nil {
		return "", err
	}
	return fmt.Sprintf("dev:%d", dev), nil
}

// FilesystemSize returns the total size in bytes of the filesystem holding
// path.
func FilesystemSize(path string) (int64, error) {
	var st syscall.Statfs_t
	if err := syscall.Statfs(path, &st); err != nil {
		return 0, fmt.Errorf("statfs %q: %w", path, err)
	}
	return int64(st.Blocks) * int64(st.Bsize), nil //nolint:gosec // filesystem sizes fit in int64
}
