package desktop

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"syscall"
)

// Start-up check failures. Each wraps with the offending path and a hint; the
// caller logs the error and exits non-zero (design sections 8.8 and 10).
var (
	ErrMountPointNotEmpty = errors.New("desktop: mount point is not empty")
	ErrMountPointMounted  = errors.New("desktop: mount point is already a mount")
	ErrMountPointNotDir   = errors.New("desktop: mount point is not a directory")
)

// isMountFunc reports whether path is a mount point.
type isMountFunc func(path string) (bool, error)

// CheckMountPoint makes sure path is a usable, empty, not-yet-mounted
// directory, creating it (mode 0700) when it does not exist. It never mounts
// over content and never stacks a second mount. isMount may be nil (the
// default compares st_dev with the parent directory).
func CheckMountPoint(path string, isMount isMountFunc) error {
	if isMount == nil {
		isMount = deviceDiffersFromParent
	}
	fi, err := os.Stat(path)
	switch {
	case errors.Is(err, os.ErrNotExist):
		if err := os.MkdirAll(path, 0o700); err != nil {
			return fmt.Errorf("desktop: create mount point %q: %w", path, err)
		}
		return nil
	case errors.Is(err, syscall.ENOTCONN):
		return fmt.Errorf("%w: %q is a stale FUSE mount (transport endpoint is not connected); "+
			"run `fusermount3 -u -z %s` first", ErrMountPointMounted, path, path)
	case err != nil:
		return fmt.Errorf("desktop: stat mount point %q: %w", path, err)
	case !fi.IsDir():
		return fmt.Errorf("%w: %q", ErrMountPointNotDir, path)
	}
	mounted, err := isMount(path)
	if err != nil {
		return fmt.Errorf("desktop: check whether %q is a mount: %w", path, err)
	}
	if mounted {
		return fmt.Errorf("%w: %q (unmount it first)", ErrMountPointMounted, path)
	}
	entries, err := os.ReadDir(path)
	if err != nil {
		return fmt.Errorf("desktop: list mount point %q: %w", path, err)
	}
	if len(entries) > 0 {
		return fmt.Errorf("%w: %q holds %d entries; refusing to mount over content", ErrMountPointNotEmpty, path, len(entries))
	}
	return nil
}

// deviceDiffersFromParent is the default isMount: a directory whose st_dev
// differs from its parent's is the root of another filesystem. It does not
// see a bind mount of the same filesystem, which cannot be an earlier
// homedrive mount.
func deviceDiffersFromParent(path string) (bool, error) {
	abs, err := filepath.Abs(path)
	if err != nil {
		return false, fmt.Errorf("absolute path: %w", err)
	}
	dev, err := statDev(abs)
	if err != nil {
		return false, err
	}
	parent, err := statDev(filepath.Dir(abs))
	if err != nil {
		return false, err
	}
	return dev != parent, nil
}

func statDev(path string) (uint64, error) {
	var st syscall.Stat_t
	if err := syscall.Stat(path, &st); err != nil {
		return 0, fmt.Errorf("stat %q: %w", path, err)
	}
	return uint64(st.Dev), nil //nolint:gosec // device numbers are opaque identifiers
}
