package rcloneclient

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"path"
	"strings"
	"time"

	"github.com/asnowfix/home-drive/homedrive/internal/pathfilter"
	"github.com/rclone/rclone/fs/config"
	"golang.org/x/oauth2"
	"golang.org/x/oauth2/google"
	drivev3 "google.golang.org/api/drive/v3"
	"google.golang.org/api/googleapi"
	"google.golang.org/api/option"
)

// initialSyncPrefix marks a page token that was minted by GetStartPageToken
// and has not yet been consumed by a full walk. ListChanges strips it and
// performs a full recursive walk instead of an incremental changes.list
// call. It is only ever held in memory or transiently in the Bolt store
// between GetStartPageToken and the following ListChanges call within the
// same poll cycle (see syncer.Puller.poll) -- a crash in between simply
// causes the full walk to be retried on restart, which is safe and
// idempotent.
const initialSyncPrefix = "initial_sync:"

// changesFields lists the change fields fetched from changes.list. Keeping
// it minimal reduces response size and API quota usage.
const changesFields = "nextPageToken,newStartPageToken," +
	"changes(fileId,removed,file(id,name,parents,mimeType,modifiedTime,md5Checksum,size,trashed))"

// remoteSectionName extracts the rclone.conf section name from a remote
// string like "gdrive:" or "gdrive:sub/dir".
func remoteSectionName(remote string) string {
	name, _, _ := strings.Cut(remote, ":")
	return name
}

// driveService lazily builds (and caches) a low-level Drive API v3 client
// authenticated with the same OAuth2 token rclone stores in rclone.conf for
// this remote. operations/fs (the allow-listed rclone packages) don't
// expose changes.getStartPageToken / changes.list, so this wraps the
// low-level googleapis client directly rather than adding it as a second,
// separate credential path (PLAN.md §7.1; homedrive-rclone-import skill).
func (r *RcloneFS) driveService(ctx context.Context) (*drivev3.Service, error) {
	r.mu.Lock()
	defer r.mu.Unlock()

	if r.changesSvc != nil {
		return r.changesSvc, nil
	}

	httpClient, err := r.oauthHTTPClient(ctx)
	if err != nil {
		return nil, fmt.Errorf("build oauth client for changes API: %w", err)
	}

	svc, err := drivev3.NewService(ctx, option.WithHTTPClient(httpClient))
	if err != nil {
		return nil, fmt.Errorf("build drive service: %w", err)
	}

	r.changesSvc = svc
	return svc, nil
}

// oauthHTTPClient builds an http.Client from the OAuth2 token rclone.conf
// already stores for r.remoteName. It reuses that stored token (and, when
// present, the remote's own client_id/client_secret) rather than starting a
// new OAuth consent flow.
func (r *RcloneFS) oauthHTTPClient(ctx context.Context) (*http.Client, error) {
	tokenJSON, ok := config.FileGetValue(r.remoteName, "token")
	if !ok || tokenJSON == "" {
		return nil, fmt.Errorf("no oauth token stored for remote %q in rclone.conf", r.remoteName)
	}
	clientID, _ := config.FileGetValue(r.remoteName, "client_id")
	clientSecret, _ := config.FileGetValue(r.remoteName, "client_secret")

	oauthCfg, tok, err := buildOAuthConfig(tokenJSON, clientID, clientSecret)
	if err != nil {
		return nil, err
	}
	if clientID == "" || clientSecret == "" {
		r.log.Warn("remote has no client_id/client_secret in rclone.conf; "+
			"Drive Changes API polling will start failing once the currently "+
			"stored access token expires -- configure a personal OAuth client "+
			"for this remote (see homedrive/README.md)",
			"remote", r.remoteName,
		)
	}
	return oauthCfg.Client(ctx, tok), nil
}

// buildOAuthConfig parses a stored rclone.conf OAuth2 token and builds the
// oauth2.Config used to authenticate our own low-level Drive API calls. It
// is a pure function (no I/O) so it is unit-testable without touching
// rclone's process-global config store.
func buildOAuthConfig(tokenJSON, clientID, clientSecret string) (*oauth2.Config, *oauth2.Token, error) {
	var tok oauth2.Token
	if err := json.Unmarshal([]byte(tokenJSON), &tok); err != nil {
		return nil, nil, fmt.Errorf("parse stored oauth token: %w", err)
	}

	cfg := &oauth2.Config{
		ClientID:     clientID,
		ClientSecret: clientSecret,
		Endpoint:     google.Endpoint,
		Scopes:       []string{drivev3.DriveScope},
	}
	return cfg, &tok, nil
}

// isGoneErr reports whether err is a Drive API HTTP 410 (Gone) response,
// signalling that a page token has expired or been invalidated.
func isGoneErr(err error) bool {
	var gerr *googleapi.Error
	return errors.As(err, &gerr) && gerr.Code == http.StatusGone
}

// pollChanges calls changes.list starting from pageToken, paginating until
// Drive reports either a NewStartPageToken (caught up) or no further pages.
func (r *RcloneFS) pollChanges(ctx context.Context, pageToken string) (Changes, error) {
	svc, err := r.driveService(ctx)
	if err != nil {
		return Changes{}, err
	}

	if err := r.ensureRootID(ctx, svc); err != nil {
		return Changes{}, fmt.Errorf("resolve sync root id: %w", err)
	}

	var items []Change
	token := pageToken
	for {
		list, err := svc.Changes.List(token).
			SupportsAllDrives(true).
			Fields(changesFields).
			Context(ctx).Do()
		if err != nil {
			if isGoneErr(err) {
				return Changes{}, fmt.Errorf("rcloneclient: changes.list: %w: %w", ErrGone, err)
			}
			return Changes{}, fmt.Errorf("rcloneclient: changes.list: %w", err)
		}

		for _, c := range list.Changes {
			if ch, ok := r.translateChange(c); ok {
				items = append(items, ch)
			}
		}

		switch {
		case list.NewStartPageToken != "":
			return Changes{Items: items, NextPageToken: list.NewStartPageToken}, nil
		case list.NextPageToken != "":
			token = list.NextPageToken
		default:
			return Changes{Items: items, NextPageToken: token}, nil
		}
	}
}

// translateChange converts a single Drive API change into our Change type,
// resolving its remote path via the ID cache. Returns ok=false when the
// change should be skipped (excluded by config, or its path cannot yet be
// resolved -- the hourly bisync safety net will catch the latter case).
func (r *RcloneFS) translateChange(c *drivev3.Change) (Change, bool) {
	if c.Removed || c.File == nil {
		p, ok := r.pathCache.get(c.FileId)
		if !ok {
			r.log.Warn("ListChanges: skipping removed file with unknown path",
				"op", "ListChanges", "file_id", c.FileId)
			return Change{}, false
		}
		return Change{Path: p, Deleted: true}, !r.excluded(p)
	}

	p, ok := r.resolveChangePath(c.File)
	if !ok {
		r.log.Warn("ListChanges: skipping change with unresolved parent path",
			"op", "ListChanges", "file_id", c.FileId, "name", c.File.Name)
		return Change{}, false
	}
	if r.excluded(p) {
		return Change{}, false
	}

	if c.File.Trashed {
		return Change{Path: p, Deleted: true}, true
	}

	r.pathCache.put(c.FileId, p)
	obj := RemoteObject{
		Path:     p,
		Size:     c.File.Size,
		ModTime:  parseDriveTime(c.File.ModifiedTime),
		MD5:      c.File.Md5Checksum,
		RemoteID: c.FileId,
	}
	return Change{Path: p, Object: &obj}, true
}

// resolveChangePath resolves a changed file's remote-relative path from its
// parent ID via the cache seeded by the full walk and prior changes.
func (r *RcloneFS) resolveChangePath(f *drivev3.File) (string, bool) {
	if len(f.Parents) == 0 {
		return f.Name, true
	}
	parentPath, ok := r.pathCache.get(f.Parents[0])
	if !ok {
		return "", false
	}
	if parentPath == "" {
		return f.Name, true
	}
	return path.Join(parentPath, f.Name), true
}

// excluded reports whether relPath matches any configured watcher.exclude
// pattern. Matching is delegated to internal/pathfilter, the single matcher
// shared with the push-side watcher (see internal/watcher/filter.go and
// homedrive/docs/migrating-rclone-filters.md).
func (r *RcloneFS) excluded(relPath string) bool {
	return pathfilter.Excluded(r.exclude, relPath)
}

// parseDriveTime parses a Drive API RFC3339 modifiedTime, returning the
// zero time on error rather than failing the whole change.
func parseDriveTime(s string) time.Time {
	t, err := time.Parse(time.RFC3339, s)
	if err != nil {
		return time.Time{}
	}
	return t
}
