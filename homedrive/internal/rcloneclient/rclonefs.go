package rcloneclient

import (
	"context"
	"fmt"
	"io"
	"log/slog"
	"os"
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

// GetStartPageToken returns the sentinel token that triggers a full
// remote walk on the first pull cycle.
func (r *RcloneFS) GetStartPageToken(_ context.Context) (string, error) {
	return "initial_sync", nil
}

// ListChanges returns remote changes since the given page token.
//
// When pageToken == "initial_sync", performs a full recursive walk of
// the remote and returns every file as a change, triggering a complete
// pull on first startup or after a token reset.
//
// Any other token returns empty changes with the same stable token
// until the Drive Changes API is wired in.
func (r *RcloneFS) ListChanges(ctx context.Context, pageToken string) (Changes, error) {
	if pageToken != "initial_sync" {
		return Changes{NextPageToken: pageToken}, nil
	}

	r.log.Info("ListChanges: full remote walk (initial sync)", "op", "ListChanges")

	var items []Change
	if err := r.listRecursive(ctx, "", &items); err != nil {
		return Changes{}, fmt.Errorf("rcloneclient: walk for initial sync: %w", err)
	}

	r.log.Info("ListChanges: walk complete", "files", len(items))
	return Changes{Items: items, NextPageToken: "synced"}, nil
}

// listRecursive walks dir on the remote Fs, appending file changes to items.
func (r *RcloneFS) listRecursive(ctx context.Context, dir string, items *[]Change) error {
	entries, err := r.fsObj.List(ctx, dir)
	if err != nil {
		return fmt.Errorf("list %q: %w", dir, err)
	}
	for _, entry := range entries {
		switch v := entry.(type) {
		case rclonefs.Object:
			ro := remoteObjectFromRclone(v)
			*items = append(*items, Change{Path: ro.Path, Object: &ro})
		default:
			if err := r.listRecursive(ctx, entry.Remote(), items); err != nil {
				return err
			}
		}
	}
	return nil
}

// DownloadFile downloads the remote file at remotePath to localPath,
// preserving the remote mtime for loop-prevention.
func (r *RcloneFS) DownloadFile(ctx context.Context, remotePath, localPath string) error {
	obj, err := r.fsObj.NewObject(ctx, remotePath)
	if err != nil {
		return fmt.Errorf("rcloneclient: open remote %q: %w", remotePath, err)
	}

	rc, err := obj.Open(ctx)
	if err != nil {
		return fmt.Errorf("rcloneclient: open stream %q: %w", remotePath, err)
	}
	defer func() { _ = rc.Close() }()

	if err := os.MkdirAll(filepath.Dir(localPath), 0o755); err != nil {
		return fmt.Errorf("rcloneclient: mkdirall for %q: %w", localPath, err)
	}

	f, err := os.Create(localPath)
	if err != nil {
		return fmt.Errorf("rcloneclient: create %q: %w", localPath, err)
	}
	defer func() { _ = f.Close() }()

	if _, err := io.Copy(f, rc); err != nil {
		return fmt.Errorf("rcloneclient: download %q: %w", localPath, err)
	}
	if err := f.Close(); err != nil {
		return fmt.Errorf("rcloneclient: close %q: %w", localPath, err)
	}

	mtime := obj.ModTime(ctx)
	if err := os.Chtimes(localPath, mtime, mtime); err != nil {
		r.log.Warn("failed to preserve mtime after download",
			"path", localPath, "error", err)
	}
	return nil
}

// List returns all objects in the given remote directory (non-recursive).
func (r *RcloneFS) List(ctx context.Context, dir string) ([]RemoteObject, error) {
	entries, err := r.fsObj.List(ctx, dir)
	if err != nil {
		return nil, fmt.Errorf("rcloneclient: list %q: %w", dir, err)
	}
	var objs []RemoteObject
	for _, entry := range entries {
		if obj, ok := entry.(rclonefs.Object); ok {
			objs = append(objs, remoteObjectFromRclone(obj))
		}
	}
	return objs, nil
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

	// MD5 extraction requires fs/hash which would add rclone backend imports
	// beyond the allowed set; omitted until the Drive Changes API needs it.

	return ro
}
