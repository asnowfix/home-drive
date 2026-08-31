// Package rcloneclient wraps the minimal set of rclone library calls needed
// for Google Drive operations. Only backend/drive is imported to keep the
// binary under 25 MB. All remote filesystem access goes through the
// RemoteFS interface so tests can use MemFS or FlakyFS.
package rcloneclient

import "errors"

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
	// issue #64 to also cover HTTP 400 responses that specifically reject
	// the pageToken parameter (see isBadPageTokenErr in driveapi.go) --
	// any error wrapping ErrGone triggers the same reset-and-full-walk
	// recovery path regardless of which underlying HTTP status caused it.
	ErrGone = errors.New("rcloneclient: page token expired (410 GONE)")

	// ErrTokenRejected additionally wraps ErrGone (never appears alone) on
	// the non-410 branch of that broadened set -- currently the HTTP 400
	// "bad pageToken" case. It exists solely so callers that want a
	// distinguishable log line or MQTT event for that case can check
	// errors.Is(err, ErrTokenRejected) in addition to the ErrGone check
	// they already do; the recovery behavior itself does not depend on it.
	ErrTokenRejected = errors.New("rcloneclient: page token rejected (non-410)")
)
