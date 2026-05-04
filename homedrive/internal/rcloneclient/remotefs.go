package rcloneclient

import (
	"context"
	"time"
)

// RemoteObject represents a file or directory on the remote filesystem.
type RemoteObject struct {
	// Path is the remote path relative to the remote root.
	Path string

	// Size in bytes.
	Size int64

	// ModTime is the last modification time reported by the remote.
	ModTime time.Time

	// MD5 checksum, empty if unavailable.
	MD5 string

	// RemoteID is the provider-specific identifier (e.g. Drive file ID).
	RemoteID string

	// DryRun indicates this object was returned by a dry-run operation
	// and does not exist on the remote.
	DryRun bool
}

// Change represents a single modification reported by the remote.
type Change struct {
	// Path is the remote path that changed.
	Path string

	// Deleted is true if the file was removed on the remote.
	Deleted bool

	// Object holds the current state if the file still exists.
	// Nil when Deleted is true.
	Object *RemoteObject
}

// Changes is the result of a ListChanges call.
type Changes struct {
	// Items contains the list of changes since the given page token.
	Items []Change

	// NextPageToken is the opaque token to pass in the next call.
	// Empty string means no more changes are available.
	NextPageToken string
}

// Quota holds remote storage usage information.
type Quota struct {
	// Used is the number of bytes consumed.
	Used int64

	// Total is the total number of bytes available (-1 if unlimited).
	Total int64
}

// UsedPercent returns the percentage of quota consumed. Returns 0 if
// total is zero or unlimited.
func (q Quota) UsedPercent() float64 {
	if q.Total <= 0 {
		return 0
	}
	return float64(q.Used) / float64(q.Total) * 100
}

// RemoteFS is the interface for all remote filesystem operations.
// The production implementation wraps rclone; tests use MemFS or FlakyFS.
type RemoteFS interface {
	// CopyFile uploads a local file to the given remote directory.
	// src is the local filesystem path; dstDir is the remote directory.
	CopyFile(ctx context.Context, src, dstDir string) (RemoteObject, error)

	// DeleteFile removes the file at the given remote path.
	DeleteFile(ctx context.Context, path string) error

	// MoveFile renames or moves a remote file from src to dst.
	MoveFile(ctx context.Context, src, dst string) error

	// Stat returns metadata for the remote object at path.
	Stat(ctx context.Context, path string) (RemoteObject, error)

	// ListChanges returns changes since the given page token.
	// Pass an empty string to get the initial page token.
	ListChanges(ctx context.Context, pageToken string) (Changes, error)

	// Quota returns the current remote storage usage.
	Quota(ctx context.Context) (Quota, error)
}
