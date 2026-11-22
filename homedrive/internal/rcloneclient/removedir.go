package rcloneclient

import (
	"context"
	"fmt"
	"strings"

	drivev3 "google.golang.org/api/drive/v3"
)

// folderMimeType is the Drive mime type of a folder.
const folderMimeType = "application/vnd.google-apps.folder"

// RemoveEmptyDir moves the Drive folder at remotePath to the Drive trash,
// only if it has no child at all (issue #174). It never deletes
// permanently and never recurses:
//
//   - the remote root ("" or ".") is refused;
//   - a missing folder is reported as ErrNotFound (the desired end state);
//   - the folder's children are listed at call time WITHOUT the usual
//     "trashed = false" filter, so a trashed child, or one homedrive does
//     not know about, makes it refuse with ErrDirNotEmpty;
//   - several folders with the same name at that path are refused with
//     ErrDirNotEmpty too: which one is "the" synced folder is unknown.
//
// The trash is a files.update(trashed=true) call made here rather than
// rclone's Rmdir, whose emptiness check ignores trashed children and which
// deletes permanently when the remote has use_trash=false.
func (r *RcloneFS) RemoveEmptyDir(ctx context.Context, remotePath string) error {
	rel := strings.Trim(remotePath, "/")
	if rel == "" || rel == "." {
		return fmt.Errorf("rcloneclient: remove dir: refusing the remote root")
	}
	svc, err := r.driveService(ctx)
	if err != nil {
		return fmt.Errorf("rcloneclient: remove dir %q: %w", rel, err)
	}
	if err := r.ensureRootID(ctx, svc); err != nil {
		return fmt.Errorf("rcloneclient: remove dir %q: %w", rel, err)
	}
	r.mu.Lock()
	rootID := r.rootID
	r.mu.Unlock()

	id, err := lookupFolderID(ctx, svc, rootID, rel)
	if err != nil {
		return fmt.Errorf("rcloneclient: remove dir %q: %w", rel, err)
	}
	if err := checkNoChildren(ctx, svc, id); err != nil {
		return fmt.Errorf("rcloneclient: remove dir %q: %w", rel, err)
	}
	_, err = svc.Files.Update(id, &drivev3.File{Trashed: true}).
		Fields("id").SupportsAllDrives(true).Context(ctx).Do()
	if err != nil {
		return fmt.Errorf("rcloneclient: trash dir %q: %w", rel, err)
	}
	r.forgetDir(id, rel)
	return nil
}

// forgetDir drops every cached mapping to the trashed folder: ours, and
// rclone's own directory cache, which would otherwise keep resolving rel to
// the trashed folder ID and upload a later file into the trash.
func (r *RcloneFS) forgetDir(id, rel string) {
	r.pathCache.delete(id)
	r.pathCache.evictTree(rel)
	if f, ok := r.fsObj.(interface{ DirCacheFlush() }); ok {
		f.DirCacheFlush()
	}
}

// lookupFolderID resolves the Drive ID of the non-trashed folder at the
// root-relative path rel, walking it segment by segment from rootID. A
// missing segment is ErrNotFound; a duplicated name is ErrDirNotEmpty.
func lookupFolderID(ctx context.Context, svc *drivev3.Service, rootID, rel string) (string, error) {
	id := rootID
	for _, seg := range strings.Split(rel, "/") {
		q := fmt.Sprintf(
			"name = '%s' and '%s' in parents and mimeType = '%s' and trashed = false",
			escapeDriveQueryValue(seg), id, folderMimeType,
		)
		list, err := svc.Files.List().Q(q).Fields("files(id)").PageSize(2).
			SupportsAllDrives(true).IncludeItemsFromAllDrives(true).
			Context(ctx).Do()
		if err != nil {
			return "", fmt.Errorf("resolve folder %q: %w", seg, err)
		}
		switch len(list.Files) {
		case 0:
			return "", fmt.Errorf("resolve folder %q: %w", seg, ErrNotFound)
		case 1:
			id = list.Files[0].Id
		default:
			return "", fmt.Errorf("several folders named %q: %w", seg, ErrDirNotEmpty)
		}
	}
	return id, nil
}

// checkNoChildren returns ErrDirNotEmpty if the folder id has any child,
// trashed or not (no "trashed = false" filter on purpose).
func checkNoChildren(ctx context.Context, svc *drivev3.Service, id string) error {
	q := fmt.Sprintf("'%s' in parents", id)
	list, err := svc.Files.List().Q(q).Fields("files(id)").PageSize(1).
		SupportsAllDrives(true).IncludeItemsFromAllDrives(true).
		Context(ctx).Do()
	if err != nil {
		return fmt.Errorf("list children: %w", err)
	}
	if len(list.Files) > 0 {
		return ErrDirNotEmpty
	}
	return nil
}
