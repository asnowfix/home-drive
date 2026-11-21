// Package tokenlock guards a token file (rclone.conf) so that two homedrive
// processes never share one. Both NAS mode (`homedrive run`) and desktop mode
// (`homedrive mount`) take an exclusive, non-blocking flock on
// "<token file>.lock" at start-up and hold it for the process lifetime; the
// kernel releases it when the process exits, however it exits.
//
// The package deliberately imports no rclone package so NAS mode can use it
// without linking anything new.
package tokenlock

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"syscall"
)

// ErrHeld is returned (wrapped) when another process holds the lock.
var ErrHeld = errors.New("tokenlock: token file is in use by another homedrive process")

// Suffix is appended to the token file path to name the lock file.
const Suffix = ".lock"

// RcloneDefaultPath mirrors where rclone looks for rclone.conf when no path
// is configured: $XDG_CONFIG_HOME/rclone/rclone.conf when xdgConfigHome is an
// absolute path, otherwise ~/.config/rclone/rclone.conf.
func RcloneDefaultPath(xdgConfigHome, home string) string {
	base := filepath.Join(home, ".config")
	if filepath.IsAbs(xdgConfigHome) {
		base = xdgConfigHome
	}
	return filepath.Join(base, "rclone", "rclone.conf")
}

// Lock is a held token-file lock.
type Lock struct {
	f *os.File
}

// Path returns the lock file path for a token file.
func Path(tokenFile string) string { return tokenFile + Suffix }

// Acquire takes the exclusive lock for tokenFile, creating the lock file (and
// its parent directory, mode 0700) if needed. It never blocks: if another
// process holds the lock it returns an error wrapping ErrHeld.
func Acquire(tokenFile string) (*Lock, error) {
	p := Path(tokenFile)
	if err := os.MkdirAll(filepath.Dir(p), 0o700); err != nil {
		return nil, fmt.Errorf("tokenlock: create %q: %w", filepath.Dir(p), err)
	}
	f, err := os.OpenFile(p, os.O_RDWR|os.O_CREATE, 0o600) //nolint:gosec // path is the operator-configured token file
	if err != nil {
		return nil, fmt.Errorf("tokenlock: open %q: %w", p, err)
	}
	if err := syscall.Flock(int(f.Fd()), syscall.LOCK_EX|syscall.LOCK_NB); err != nil { //nolint:gosec // fd fits in int
		_ = f.Close()
		if errors.Is(err, syscall.EWOULDBLOCK) {
			return nil, fmt.Errorf("%w: lock file %q", ErrHeld, p)
		}
		return nil, fmt.Errorf("tokenlock: flock %q: %w", p, err)
	}
	return &Lock{f: f}, nil
}

// Release drops the lock. It is safe on a nil Lock and idempotent.
func (l *Lock) Release() error {
	if l == nil || l.f == nil {
		return nil
	}
	err := l.f.Close() // closing the descriptor releases the flock
	l.f = nil
	if err != nil {
		return fmt.Errorf("tokenlock: release: %w", err)
	}
	return nil
}
