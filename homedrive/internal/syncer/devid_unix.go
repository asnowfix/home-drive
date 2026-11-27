//go:build unix

package syncer

import (
	"os"
	"syscall"
)

// sameDevice reports whether a and b live on the same filesystem (st_dev).
func sameDevice(a, b os.FileInfo) bool {
	sa, ok1 := a.Sys().(*syscall.Stat_t)
	sb, ok2 := b.Sys().(*syscall.Stat_t)
	if !ok1 || !ok2 {
		return false
	}
	return uint64(sa.Dev) == uint64(sb.Dev) //nolint:unconvert // Dev is int32 on darwin
}
