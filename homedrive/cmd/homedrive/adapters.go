package main

import (
	"context"
	"log/slog"
	"os"
	"strings"

	"github.com/asnowfix/home-drive/homedrive/internal/rcloneclient"
	"github.com/asnowfix/home-drive/homedrive/internal/store"
	"github.com/asnowfix/home-drive/homedrive/internal/syncer"
	"github.com/asnowfix/home-drive/homedrive/internal/watcher"
)

// remoteFS is the union of every remote filesystem method the sync engine
// needs: everything syncer.RemoteFS requires (via rcloneSyncerAdapter),
// plus Quota for /status and health reporting. *rcloneclient.RcloneFS
// implements it in production. Defining it here (rather than importing
// rcloneclient.RemoteFS, which omits List/GetStartPageToken/DownloadFile,
// or syncer.RemoteFS, which omits Quota) lets Agent -- and the whole
// wiring layer in this package -- be constructed against a test double in
// unit tests, per the homedrive-test-mocks skill: never call rclone
// directly in tests.
type remoteFS interface {
	CopyFile(ctx context.Context, src, dstDir string) (rcloneclient.RemoteObject, error)
	DeleteFile(ctx context.Context, path string) error
	MoveFile(ctx context.Context, src, dst string) error
	Stat(ctx context.Context, path string) (rcloneclient.RemoteObject, error)
	List(ctx context.Context, dir string) ([]rcloneclient.RemoteObject, error)
	ListChanges(ctx context.Context, pageToken string) (rcloneclient.Changes, error)
	GetStartPageToken(ctx context.Context) (string, error)
	DownloadFile(ctx context.Context, remotePath, localPath string) error
	Quota(ctx context.Context) (rcloneclient.Quota, error)
}

// ---------------------------------------------------------------------------
// rcloneSyncerAdapter: remoteFS → syncer.RemoteFS
// ---------------------------------------------------------------------------

type rcloneSyncerAdapter struct {
	fs remoteFS
}

func toSyncerObject(ro rcloneclient.RemoteObject) syncer.RemoteObject {
	return syncer.RemoteObject{
		Path:    ro.Path,
		Size:    ro.Size,
		MD5:     ro.MD5,
		ModTime: ro.ModTime,
		ID:      ro.RemoteID,
	}
}

func (a *rcloneSyncerAdapter) CopyFile(ctx context.Context, src, dstDir string) (syncer.RemoteObject, error) {
	ro, err := a.fs.CopyFile(ctx, src, dstDir)
	return toSyncerObject(ro), err
}

func (a *rcloneSyncerAdapter) DeleteFile(ctx context.Context, path string) error {
	return a.fs.DeleteFile(ctx, path)
}

func (a *rcloneSyncerAdapter) MoveFile(ctx context.Context, src, dst string) error {
	return a.fs.MoveFile(ctx, src, dst)
}

func (a *rcloneSyncerAdapter) Stat(ctx context.Context, path string) (syncer.RemoteObject, error) {
	ro, err := a.fs.Stat(ctx, path)
	return toSyncerObject(ro), err
}

func (a *rcloneSyncerAdapter) List(ctx context.Context, dir string) ([]syncer.RemoteObject, error) {
	ros, err := a.fs.List(ctx, dir)
	if err != nil {
		return nil, err
	}
	result := make([]syncer.RemoteObject, len(ros))
	for i, ro := range ros {
		result[i] = toSyncerObject(ro)
	}
	return result, nil
}

func (a *rcloneSyncerAdapter) ListChanges(ctx context.Context, pageToken string) (syncer.Changes, error) {
	ch, err := a.fs.ListChanges(ctx, pageToken)
	if err != nil {
		return syncer.Changes{}, err
	}
	items := make([]syncer.Change, len(ch.Items))
	for i, c := range ch.Items {
		sc := syncer.Change{Path: c.Path, Deleted: c.Deleted}
		if c.Object != nil {
			sc.Object = toSyncerObject(*c.Object)
		}
		items[i] = sc
	}
	return syncer.Changes{Items: items, NextPageToken: ch.NextPageToken}, nil
}

func (a *rcloneSyncerAdapter) GetStartPageToken(ctx context.Context) (string, error) {
	return a.fs.GetStartPageToken(ctx)
}

func (a *rcloneSyncerAdapter) DownloadFile(ctx context.Context, remotePath, localPath string) error {
	return a.fs.DownloadFile(ctx, remotePath, localPath)
}

// ---------------------------------------------------------------------------
// journalSyncerAdapter: *store.Journal → syncer.Store
// ---------------------------------------------------------------------------

type journalSyncerAdapter struct {
	j      *store.Journal
	logger *slog.Logger
}

func toSyncerJournalEntry(e store.JournalEntry) syncer.JournalEntry {
	return syncer.JournalEntry{
		Path:         e.Path,
		LocalMtime:   e.LocalMtime,
		RemoteMtime:  e.RemoteMtime,
		RemoteMD5:    e.RemoteMD5,
		RemoteID:     e.RemoteID,
		LastSyncedAt: e.LastSyncedAt,
		LastOrigin:   e.LastOrigin,
	}
}

func toStoreJournalEntry(e syncer.JournalEntry) store.JournalEntry {
	return store.JournalEntry{
		Path:         e.Path,
		LocalMtime:   e.LocalMtime,
		RemoteMtime:  e.RemoteMtime,
		RemoteMD5:    e.RemoteMD5,
		RemoteID:     e.RemoteID,
		LastSyncedAt: e.LastSyncedAt,
		LastOrigin:   e.LastOrigin,
	}
}

func (a *journalSyncerAdapter) GetPageToken(_ context.Context) (string, error) {
	return a.j.GetPageToken()
}

func (a *journalSyncerAdapter) SetPageToken(_ context.Context, token string) error {
	return a.j.SetPageToken(token)
}

func (a *journalSyncerAdapter) Get(_ context.Context, path string) (syncer.JournalEntry, bool, error) {
	e, err := a.j.Get(path)
	if err == store.ErrNotFound {
		return syncer.JournalEntry{}, false, nil
	}
	if err != nil {
		return syncer.JournalEntry{}, false, err
	}
	return toSyncerJournalEntry(e), true, nil
}

func (a *journalSyncerAdapter) Put(_ context.Context, entry syncer.JournalEntry) error {
	return a.j.Put(toStoreJournalEntry(entry))
}

func (a *journalSyncerAdapter) Delete(_ context.Context, path string) error {
	return a.j.Delete(path)
}

func (a *journalSyncerAdapter) NextOldN(_ context.Context, path string) (int, error) {
	return a.j.NextOldN(path), nil
}

func (a *journalSyncerAdapter) RewritePrefix(_ context.Context, oldPrefix, newPrefix string) (int, error) {
	return store.RewritePrefix(a.j, oldPrefix, newPrefix, nil, a.logger)
}

// ---------------------------------------------------------------------------
// noopPublisher: satisfies syncer.Publisher when MQTT is disabled
// ---------------------------------------------------------------------------

type noopPublisher struct{}

func (noopPublisher) PublishJSON(_ string, _ any) error { return nil }
func (noopPublisher) Topic(parts ...string) string {
	return strings.Join(parts, "/")
}

// ---------------------------------------------------------------------------
// auditLoggerAdapter: *store.Auditor → syncer.AuditLogger
// ---------------------------------------------------------------------------

type auditLoggerAdapter struct {
	a *store.Auditor
}

func (al *auditLoggerAdapter) Log(entry syncer.AuditEntry) error {
	al.a.Log(store.AuditEntry{
		Timestamp: entry.Timestamp,
		Op:        entry.Op,
		Path:      entry.Path,
		DryRun:    entry.DryRun,
		Error:     entry.Error,
	})
	return nil
}

// ---------------------------------------------------------------------------
// watcherStoreAdapter: *store.Journal → watcher.SyncStore
// ---------------------------------------------------------------------------

// watcherStoreAdapter bridges *store.Journal to watcher.SyncStore for the
// inode/mtime self-induced-echo guard (PLAN.md §7.3: "the syncer ignores
// any incoming watcher event whose mtime matches the last-recorded
// local_mtime for that path, within 1s tolerance" -- a mtime-only check).
//
// watcher.SyncRecord additionally carries a Size field so the watcher can
// require an exact size match too, but store.JournalEntry (by design,
// per PLAN.md §7.3) does not persist a file size. This adapter fills Size
// from a fresh os.Stat of the same path the watcher is about to compare
// against, which makes that term of the comparison trivially true and
// correctly reduces the guard to the documented mtime-only check without
// requiring a schema change to the journal.
type watcherStoreAdapter struct {
	j *store.Journal
}

func (a *watcherStoreAdapter) GetSyncRecord(path string) *watcher.SyncRecord {
	entry, err := a.j.Get(path)
	if err != nil {
		return nil
	}
	info, err := os.Stat(path)
	if err != nil {
		return nil
	}
	return &watcher.SyncRecord{LocalMtime: entry.LocalMtime, Size: info.Size()}
}

// ---------------------------------------------------------------------------
// bisyncJournalAdapter: *store.Journal → syncer.Journal (bisync)
// ---------------------------------------------------------------------------

// bisyncJournalAdapter bridges *store.Journal to the syncer.Journal
// interface used by the bisync safety net. It is distinct from
// journalSyncerAdapter because syncer.Journal (bisync) is context-free
// and returns a pointer entry, while syncer.Store (push/pull) is
// context-aware and returns (entry, bool, error).
type bisyncJournalAdapter struct {
	j *store.Journal
}

func (a *bisyncJournalAdapter) Get(path string) (*syncer.JournalEntry, error) {
	e, err := a.j.Get(path)
	if err != nil {
		// Callers (bisync_ops.go: hasDivergence) treat any error the same
		// as "no entry" and fall back to a direct local/remote compare.
		return nil, err
	}
	se := toSyncerJournalEntry(e)
	return &se, nil
}

func (a *bisyncJournalAdapter) Put(entry syncer.JournalEntry) error {
	return a.j.Put(toStoreJournalEntry(entry))
}

func (a *bisyncJournalAdapter) Exists(path string) bool {
	return a.j.Exists(path)
}
