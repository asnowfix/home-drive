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

	// ErrGone indicates the Drive Changes API page token has expired or is
	// otherwise invalid (HTTP 410). Callers must obtain a fresh start page
	// token via GetStartPageToken and retry (PLAN.md §7.1).
	ErrGone = errors.New("rcloneclient: page token expired (410 GONE)")
)
