//go:build linux

package desktop

import (
	"fmt"
	"syscall"
)

// FilesystemID returns an identifier of the filesystem holding path: the
// statfs f_fsid (derived from the filesystem UUID on ext4, xfs and btrfs, so
// stable across reboots, unlike st_dev) or, when the kernel reports none
// (some FUSE and network filesystems), the empty string: st_dev there is an
// anonymous number that changes across reboots, so it must not be recorded.
// CheckCacheDir skips the comparison for an empty ID.
func FilesystemID(path string) (string, error) {
	var st syscall.Statfs_t
	if err := syscall.Statfs(path, &st); err != nil {
		return "", fmt.Errorf("statfs %q: %w", path, err)
	}
	if st.Fsid.X__val[0] != 0 || st.Fsid.X__val[1] != 0 {
		return fmt.Sprintf("fsid:%x-%x", uint32(st.Fsid.X__val[0]), uint32(st.Fsid.X__val[1])), nil //nolint:gosec // bit pattern, not a value
	}
	return "", nil
}
