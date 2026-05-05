package rcloneclient

import (
	"context"
	"fmt"
	"log/slog"
	"path/filepath"

	// Single rclone backend -- keep this as the ONLY backend import.
	// Verify with: go tool nm <binary> | grep -c rclone/backend/
	_ "github.com/rclone/rclone/backend/drive"

	rclonefs "github.com/rclone/rclone/fs"
	"github.com/rclone/rclone/fs/config"
	"github.com/rclone/rclone/fs/config/configfile"
	"github.com/rclone/rclone/fs/operations"
)

func init() {
	// Install the rclone config file handler so that rclone.conf is
	// loaded automatically when NewFs is called.
	configfile.Install()
}

// RcloneFSConfig holds the configuration for the production rclone wrapper.
type RcloneFSConfig struct {
	// Remote is the rclone remote name with colon, e.g. "gdrive:".
	Remote string

	// ConfigPath is the path to rclone.conf.
	ConfigPath string

	// Log is the structured logger.
	Log *slog.Logger
}

// RcloneFS is the production implementation of RemoteFS backed by rclone
// libraries. It imports only backend/drive to keep the binary small.
type RcloneFS struct {
	remote string
	fsObj  rclonefs.Fs
	log    *slog.Logger
}

// NewRcloneFS initializes the rclone backend from the given config.
// It loads rclone.conf and creates the remote Fs object.
func NewRcloneFS(ctx context.Context, cfg RcloneFSConfig) (*RcloneFS, error) {
	if cfg.ConfigPath != "" {
		if err := config.SetConfigPath(cfg.ConfigPath); err != nil {
			return nil, fmt.Errorf("rcloneclient: set config path %q: %w", cfg.ConfigPath, err)
		}
	}

	remote := cfg.Remote
	if remote == "" {
		return nil, fmt.Errorf("rcloneclient: remote is required")
	}

	log := cfg.Log
	if log == nil {
		log = slog.Default()
	}

	fsObj, err := rclonefs.NewFs(ctx, remote)
	if err != nil {
		return nil, fmt.Errorf("rcloneclient: init remote %q: %w", remote, err)
	}

	log.Info("rclone remote initialized",
		"remote", remote,
		"config_path", cfg.ConfigPath,
	)

	return &RcloneFS{
		remote: remote,
		fsObj:  fsObj,
		log:    log,
	}, nil
}

// CopyFile uploads a local file to the remote directory.
func (r *RcloneFS) CopyFile(ctx context.Context, src, dstDir string) (RemoteObject, error) {
	dstRemote := dstDir + "/" + filepath.Base(src)

	dstObj, err := operations.CopyURL(ctx, r.fsObj, dstRemote, src, false, false, false)
	if err != nil {
		return RemoteObject{}, fmt.Errorf("rcloneclient: copy %q to %q: %w", src, dstRemote, err)
	}

	return remoteObjectFromRclone(dstObj), nil
}

// DeleteFile removes the file at the given remote path.
func (r *RcloneFS) DeleteFile(ctx context.Context, remotePath string) error {
	obj, err := r.fsObj.NewObject(ctx, remotePath)
	if err != nil {
		return fmt.Errorf("rcloneclient: delete lookup %q: %w", remotePath, err)
	}

	if err := operations.DeleteFile(ctx, obj); err != nil {
		return fmt.Errorf("rcloneclient: delete %q: %w", remotePath, err)
	}
	return nil
}

// MoveFile renames or moves a remote file from src to dst.
func (r *RcloneFS) MoveFile(ctx context.Context, src, dst string) error {
	srcObj, err := r.fsObj.NewObject(ctx, src)
	if err != nil {
		return fmt.Errorf("rcloneclient: move lookup %q: %w", src, err)
	}

	_, err = operations.Move(ctx, r.fsObj, nil, dst, srcObj)
	if err != nil {
		return fmt.Errorf("rcloneclient: move %q to %q: %w", src, dst, err)
	}
	return nil
}

// Stat returns metadata for the remote object at path.
func (r *RcloneFS) Stat(ctx context.Context, remotePath string) (RemoteObject, error) {
	obj, err := r.fsObj.NewObject(ctx, remotePath)
	if err != nil {
		return RemoteObject{}, fmt.Errorf("rcloneclient: stat %q: %w", remotePath, err)
	}
	return remoteObjectFromRclone(obj), nil
}

// ListChanges returns changes since the given page token.
//
// This method uses the Drive Changes API, which requires casting the rclone
// Fs to the drive-specific type. This is the one place where the wrapper
// abstraction leaks -- the cast is necessary because rclone's operations
// package does not expose the Changes API generically.
//
// For Phase 2 the implementation returns a placeholder; the full Drive
// Changes API integration lands in Phase 6.
func (r *RcloneFS) ListChanges(_ context.Context, pageToken string) (Changes, error) {
	// TODO(phase-6): implement via drive.Fs cast for Changes API.
	r.log.Info("ListChanges not yet implemented",
		"op", "ListChanges",
		"page_token", pageToken,
	)
	return Changes{NextPageToken: pageToken}, nil
}

// Quota returns the current remote storage usage via fs.About.
func (r *RcloneFS) Quota(ctx context.Context) (Quota, error) {
	abouter, ok := r.fsObj.(rclonefs.Abouter)
	if !ok {
		return Quota{}, fmt.Errorf("rcloneclient: remote does not support About")
	}

	usage, err := abouter.About(ctx)
	if err != nil {
		return Quota{}, fmt.Errorf("rcloneclient: quota: %w", err)
	}

	q := Quota{}
	if usage.Used != nil {
		q.Used = *usage.Used
	}
	if usage.Total != nil {
		q.Total = *usage.Total
	} else {
		q.Total = -1
	}

	return q, nil
}

// remoteObjectFromRclone converts an rclone Object to our RemoteObject type.
func remoteObjectFromRclone(obj rclonefs.Object) RemoteObject {
	ro := RemoteObject{
		Path:    obj.Remote(),
		Size:    obj.Size(),
		ModTime: obj.ModTime(context.Background()),
	}

	// Extract the remote ID if the object supports it.
	if ider, ok := obj.(rclonefs.IDer); ok {
		ro.RemoteID = ider.ID()
	}

	// MD5 extraction deferred -- requires fs/hash import which is not in
	// the allow-list. Will be added when Phase 6 (Pull via Changes API)
	// needs it, with binary size verification.

	return ro
}
