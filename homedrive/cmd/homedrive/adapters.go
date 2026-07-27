package main

import (
	"os"
	"strings"

	"github.com/asnowfix/home-drive/homedrive/internal/store"
	"github.com/asnowfix/home-drive/homedrive/internal/syncer"
	"github.com/asnowfix/home-drive/homedrive/internal/watcher"
)

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
// store.JournalStore (which implements the canonical store.Store used by
// the push/pull syncer) because syncer.Journal (bisync) is context-free
// and returns a pointer entry, while Store is context-aware and returns
// (entry, bool, error).
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
	return &e, nil
}

func (a *bisyncJournalAdapter) Put(entry syncer.JournalEntry) error {
	return a.j.Put(entry)
}

func (a *bisyncJournalAdapter) Exists(path string) bool {
	return a.j.Exists(path)
}

func (a *bisyncJournalAdapter) Delete(path string) error {
	return a.j.Delete(path)
}

func (a *bisyncJournalAdapter) ListByPrefix(prefix string) ([]syncer.JournalEntry, error) {
	return a.j.ListByPrefix(prefix)
}

func (a *bisyncJournalAdapter) ForEach(fn func(syncer.JournalEntry) error) error {
	return a.j.ForEach(fn)
}

func (a *bisyncJournalAdapter) GetMeta(key []byte) (string, error) {
	return a.j.GetMeta(key)
}

func (a *bisyncJournalAdapter) SetMeta(key []byte, val string) error {
	return a.j.SetMeta(key, val)
}
