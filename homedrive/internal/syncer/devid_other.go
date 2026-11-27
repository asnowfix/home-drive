//go:build !unix

package syncer

import "os"

// sameDevice cannot be determined here, so the opt-in never applies.
func sameDevice(_, _ os.FileInfo) bool { return false }
