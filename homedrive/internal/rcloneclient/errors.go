// Package rcloneclient wraps the minimal set of rclone library calls needed
// for Google Drive operations. Only backend/drive is imported to keep the
// binary under 25 MB. All remote filesystem access goes through the
// RemoteFS interface so tests can use MemFS or FlakyFS.
package rcloneclient

import (
	"errors"
	"fmt"
)

// Sentinel errors for the rcloneclient package.
var (
	// ErrNotFound indicates the requested remote object does not exist.
	ErrNotFound = errors.New("remote object not found")

	// ErrQuotaExhausted indicates the remote storage quota is full.
	ErrQuotaExhausted = errors.New("remote quota exhausted")

	// ErrPermissionDenied indicates insufficient permissions for the operation.
	ErrPermissionDenied = errors.New("permission denied")

	// ErrNetworkUnavailable indicates a transient network failure.
	ErrNetworkUnavailable = errors.New("network unavailable")

	// ErrOAuthExpired indicates the OAuth token has expired and refresh failed.
	ErrOAuthExpired = errors.New("oauth token expired")

	// ErrAlreadyExists indicates a destination path already exists.
	ErrAlreadyExists = errors.New("remote object already exists")

	// ErrGone indicates the Drive Changes API page token cannot be used and
	// the caller must obtain a fresh start page token via GetStartPageToken
	// and retry (PLAN.md §7.1). Originally only HTTP 410 GONE; broadened by
	// issue #64 to also cover HTTP 400 responses from changes.list (see
	// isBadPageTokenErr in driveapi.go for why a 400 there is always
	// treated as token-related) -- any error wrapping ErrGone triggers the
	// same reset-and-full-walk recovery path regardless of which
	// underlying HTTP status caused it.
	ErrGone = errors.New("rcloneclient: page token expired (410 GONE)")

	// ErrTokenRejected marks the non-410 branch of ErrGone's broadened set
	// -- currently the HTTP 400 case. Build it only via NewTokenRejectedErr
	// below, never by hand: that keeps it always co-occurring with ErrGone
	// (see NewTokenRejectedErr's doc comment for why that matters).
	ErrTokenRejected = errors.New("rcloneclient: page token rejected (non-410)")

	// ErrOAuthClientMissing indicates a Drive Changes API call failed
	// while silently refreshing an expired OAuth access token because
	// this remote's rclone.conf section has no client_id/client_secret.
	// Google's token endpoint then returns RFC 6749's "invalid_request"
	// ("Could not determine client ID from request"), surfaced to Go as
	// *oauth2.RetrieveError -- see isOAuthClientMissingErr in
	// oauthstatus.go for exactly how this is classified.
	//
	// This is a permanent condition: retrying does not help, only an
	// operator configuring a personal OAuth client for the remote and
	// re-authorising it does (homedrive/README.md's prerequisites). That
	// is why syncer.Puller backs off its poll interval on this specific
	// error class instead of retrying at cfg.Interval indefinitely
	// (issue #67) -- deliberately narrower than "back off on any auth
	// failure", see isOAuthClientMissingErr's doc comment for why.
	//
	// The same underlying precondition -- known from rclone.conf, not
	// from this error -- is exposed to GET /healthz without an extra
	// live probe via RcloneFS.OAuthStatus.
	ErrOAuthClientMissing = errors.New("rcloneclient: oauth client_id/client_secret not configured for remote")
)

// NewTokenRejectedErr builds the error pollChanges returns when Drive
// rejects a page token for a reason other than the classic HTTP 410 GONE
// (currently: any HTTP 400 from changes.list, see isBadPageTokenErr). It
// always wraps both ErrGone -- so every existing errors.Is(err, ErrGone)
// reset-path check (in particular syncer.Puller.fetchChanges) fires
// unchanged -- and ErrTokenRejected, so that same caller can additionally
// distinguish this case for logging/MQTT purposes via errors.Is(err,
// ErrTokenRejected).
//
// Composing the two sentinels by hand at each call site, instead of
// through this single constructor, is exactly the kind of place a future
// change could accidentally return ErrTokenRejected without ErrGone --
// silently disabling the reset instead of just producing a garbled log
// line, since fetchChanges's recovery branch is gated on ErrGone. Both the
// production call site (driveapi.go's pollChanges) and the syncer test
// mocks build this error only through this constructor for that reason.
// fetchChanges also defensively treats errors.Is(err, ErrTokenRejected)
// alone as reset-worthy, as a second line of defense against the same
// mistake (issue #64 PR review item 2).
func NewTokenRejectedErr(cause error) error {
	return fmt.Errorf("%w: %w: %w", ErrGone, ErrTokenRejected, cause)
}
