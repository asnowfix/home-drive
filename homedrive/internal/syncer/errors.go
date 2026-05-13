package syncer

import "errors"

// Sentinel errors for the syncer package.
var (
	// ErrGone signals that the Drive Changes API returned HTTP 410,
	// meaning the stored page token is no longer valid and must be reset.
	ErrGone = errors.New("syncer: page token expired (410 GONE)")

	// ErrConflict signals a sync conflict where local and remote state
	// diverged from what the journal expected.
	ErrConflict = errors.New("syncer: conflict detected")

	// ErrDryRun signals that the operation was skipped due to dry-run mode.
	ErrDryRun = errors.New("syncer: dry-run, operation skipped")
)
