package rcloneclient

import (
	"context"
	"log/slog"
	"path"
)

// DryRunFS wraps a RemoteFS and logs intended writes without executing
// them. Read operations (Stat, ListChanges, Quota) pass through to the
// inner implementation.
type DryRunFS struct {
	inner RemoteFS
	log   *slog.Logger
}

// NewDryRunFS creates a DryRunFS that logs writes and delegates reads
// to the given inner RemoteFS.
func NewDryRunFS(inner RemoteFS, log *slog.Logger) *DryRunFS {
	return &DryRunFS{
		inner: inner,
		log:   log.With("dry_run", true),
	}
}

// CopyFile logs the intended copy and returns a synthetic RemoteObject.
func (d *DryRunFS) CopyFile(_ context.Context, src, dstDir string) (RemoteObject, error) {
	remotePath := path.Join(dstDir, path.Base(src))
	d.log.Info("would copy file",
		"op", "CopyFile",
		"src", src,
		"dst", dstDir,
		"path", remotePath,
	)
	return RemoteObject{
		Path:   remotePath,
		DryRun: true,
	}, nil
}

// DeleteFile logs the intended deletion and returns success.
func (d *DryRunFS) DeleteFile(_ context.Context, remotePath string) error {
	d.log.Info("would delete file",
		"op", "DeleteFile",
		"path", remotePath,
	)
	return nil
}

// MoveFile logs the intended move and returns success.
func (d *DryRunFS) MoveFile(_ context.Context, src, dst string) error {
	d.log.Info("would move file",
		"op", "MoveFile",
		"src", src,
		"dst", dst,
	)
	return nil
}

// Stat delegates to the inner RemoteFS (read-only, safe in dry-run).
func (d *DryRunFS) Stat(ctx context.Context, remotePath string) (RemoteObject, error) {
	return d.inner.Stat(ctx, remotePath)
}

// ListChanges delegates to the inner RemoteFS (read-only, safe in dry-run).
func (d *DryRunFS) ListChanges(ctx context.Context, pageToken string) (Changes, error) {
	return d.inner.ListChanges(ctx, pageToken)
}

// Quota delegates to the inner RemoteFS (read-only, safe in dry-run).
func (d *DryRunFS) Quota(ctx context.Context) (Quota, error) {
	return d.inner.Quota(ctx)
}
