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

	// ErrTokenRejected is the same sentinel as rcloneclient.ErrTokenRejected
	// (see ErrGone above for why no adapter is used): it always accompanies
	// ErrGone, distinguishing a non-410 "Drive never recognized this
	// token" rejection (currently HTTP 400) from the classic 410 GONE case
	// for logging/MQTT purposes only -- fetchChanges's reset path fires the
	// same way either way (issue #64).
	ErrTokenRejected = rcloneclient.ErrTokenRejected

	// ErrRemoteNotFound is the same sentinel as rcloneclient.ErrNotFound
	// (see ErrGone above for why no adapter is used), used by the
	// retention GC to treat "already deleted" as a successful, idempotent
	// eviction (PLAN.md §11.5).
	ErrRemoteNotFound = rcloneclient.ErrNotFound

	// ErrOAuthClientMissing is the same sentinel as
	// rcloneclient.ErrOAuthClientMissing (see ErrGone above for why no
	// adapter is used): a Drive Changes API token refresh failed because
	// the remote has no client_id/client_secret configured. Puller's
	// fetchChanges classifies this from RemoteFS.ListChanges's returned
	// error and backs off the poll interval instead of retrying at
	// cfg.Interval indefinitely (issue #67).
	ErrOAuthClientMissing = rcloneclient.ErrOAuthClientMissing

	// ErrConflict signals a sync conflict where local and remote state
	// diverged from what the journal expected.
	ErrConflict = errors.New("syncer: conflict detected")

	// ErrDryRun signals that the operation was skipped due to dry-run mode.
	ErrDryRun = errors.New("syncer: dry-run, operation skipped")
)
