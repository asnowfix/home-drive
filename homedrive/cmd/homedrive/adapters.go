package main

import (
	"context"
	"log/slog"
	"strings"

	"github.com/asnowfix/home-drive/homedrive/internal/rcloneclient"
	"github.com/asnowfix/home-drive/homedrive/internal/store"
	"github.com/asnowfix/home-drive/homedrive/internal/syncer"
)

// ---------------------------------------------------------------------------
// rcloneSyncerAdapter: *rcloneclient.RcloneFS → syncer.RemoteFS
// ---------------------------------------------------------------------------

type rcloneSyncerAdapter struct {
	fs *rcloneclient.RcloneFS
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
