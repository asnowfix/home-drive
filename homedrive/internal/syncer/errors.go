package syncer

import (
	"errors"

	"github.com/asnowfix/home-drive/homedrive/internal/rcloneclient"
)

// Sentinel errors for the syncer package.
var (
	// ErrGone signals that the Drive Changes API returned HTTP 410,
	// meaning the stored page token is no longer valid and must be reset.
	// This is the same sentinel as rcloneclient.ErrGone (not a separate
	// value translated at a package boundary): RemoteFS is now the
	// canonical rcloneclient.RemoteFS interface, so the real production
	// error already wraps rcloneclient.ErrGone and errors.Is(err, ErrGone)
	// must match it directly, with no adapter in between.
	ErrGone = rcloneclient.ErrGone

	// ErrConflict signals a sync conflict where local and remote state
	// diverged from what the journal expected.
	ErrConflict = errors.New("syncer: conflict detected")

	// ErrDryRun signals that the operation was skipped due to dry-run mode.
	ErrDryRun = errors.New("syncer: dry-run, operation skipped")
)
