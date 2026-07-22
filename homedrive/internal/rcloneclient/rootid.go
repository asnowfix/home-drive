package rcloneclient

import (
	"context"
	"fmt"
	"strings"

	drivev3 "google.golang.org/api/drive/v3"
)

// ensureRootID resolves the Drive folder ID of the configured sync root
// (r.fsObj.Root()) and seeds it into r.pathCache mapped to the empty
// relative path, so that top-level changes -- whose Parents[0] is the
// sync root's real Drive ID -- can be resolved to a relative path by
// resolveChangePath. It resolves once per process and caches the result.
//
// This mirrors what rclone's own dircache builds internally for the drive
// backend, but that cache is a private field of *drive.Fs and not exported,
// so ListChanges maintains its own minimal id-to-path cache instead of
// reaching into rclone internals.
func (r *RcloneFS) ensureRootID(ctx context.Context, svc *drivev3.Service) error {
	r.mu.Lock()
	defer r.mu.Unlock()

	if r.rootID != "" {
		return nil
	}

	id, err := resolveRootID(ctx, svc, r.fsObj.Root())
	if err != nil {
		// Not cached: a transient failure here should not permanently
		// disable Changes API polling, so the next poll cycle retries.
		return err
	}

	r.rootID = id
	r.pathCache.put(id, "")
	return nil
}

// resolveRootID walks the configured root path (relative to My Drive)
// segment by segment, resolving each folder's real Drive ID via the Drive
// API. An empty root resolves to the real ID behind the "root" alias
// (Drive never reports "root" itself as a parent ID in change/file
// resources, only the special alias accepts it as a request value).
func resolveRootID(ctx context.Context, svc *drivev3.Service, root string) (string, error) {
	rel := strings.Trim(root, "/")
	if rel == "" || rel == "." {
		f, err := svc.Files.Get("root").Fields("id").SupportsAllDrives(true).Context(ctx).Do()
		if err != nil {
			return "", fmt.Errorf("resolve My Drive root id: %w", err)
		}
		return f.Id, nil
	}

	parentID := "root"
	var id string
	for _, seg := range strings.Split(rel, "/") {
		found, err := findChildFolderID(ctx, svc, parentID, seg)
		if err != nil {
			return "", err
		}
		id = found
		parentID = id
	}
	return id, nil
}

// findChildFolderID looks up the Drive ID of the folder named name whose
// parent is parentID.
func findChildFolderID(ctx context.Context, svc *drivev3.Service, parentID, name string) (string, error) {
	q := fmt.Sprintf(
		"name = '%s' and '%s' in parents and mimeType = 'application/vnd.google-apps.folder' and trashed = false",
		escapeDriveQueryValue(name), parentID,
	)
	list, err := svc.Files.List().Q(q).Fields("files(id)").
		SupportsAllDrives(true).IncludeItemsFromAllDrives(true).
		Context(ctx).Do()
	if err != nil {
		return "", fmt.Errorf("resolve root segment %q: %w", name, err)
	}
	if len(list.Files) == 0 {
		return "", fmt.Errorf("resolve root segment %q: not found under parent %q", name, parentID)
	}
	return list.Files[0].Id, nil
}

// escapeDriveQueryValue escapes a literal value for use inside a Drive API
// query string (backslash and single-quote must be backslash-escaped).
func escapeDriveQueryValue(s string) string {
	s = strings.ReplaceAll(s, `\`, `\\`)
	s = strings.ReplaceAll(s, `'`, `\'`)
	return s
}
