package stats

import (
	"encoding/json"
	"fmt"
	"time"

	"github.com/asnowfix/home-drive/homedrive/pkg/homedrive"
)

// Meta keys (all under store.StatsMetaPrefix, "stats.").
const (
	metaLastPush  = "stats.last.push"
	metaLastPull  = "stats.last.pull"
	metaLastPoll  = "stats.last.poll"
	metaLastError = "stats.last.error"
	metaBisync    = "stats.last.bisync"
	metaLocal     = "stats.last.local"
)

// Flush writes every dirty bucket and the meta values in one Bolt
// transaction and prunes buckets older than Retention. The state is
// snapshotted under t.mu but the (fsync-bound) save runs outside it, so
// Record is never blocked by disk I/O. On failure the dirty state is
// restored so the next Flush retries; the error is returned (callers log
// it with op=stats_flush).
func (t *Tracker) Flush() error {
	if t == nil || t.cfg.Store == nil {
		return nil
	}
	t.flushMu.Lock()
	defer t.flushMu.Unlock()

	puts, starts, meta, cutoff, err := t.prepareFlush()
	if err != nil {
		return err
	}
	if err := t.cfg.Store.SaveStats(puts, bucketKey(cutoff), meta); err != nil {
		t.mu.Lock()
		for _, st := range starts {
			t.dirty[st] = struct{}{}
		}
		t.metaDirty = t.metaDirty || meta != nil
		t.mu.Unlock()
		return fmt.Errorf("stats: save: %w", err)
	}
	return nil
}

// prepareFlush prunes, then takes the dirty set (clearing it) and renders
// the rows to write. Restoring on a failed save is the caller's job.
func (t *Tracker) prepareFlush() (puts map[string][]byte, starts []time.Time, meta map[string]string, cutoff time.Time, err error) {
	t.mu.Lock()
	defer t.mu.Unlock()

	cutoff = bucketStart(t.cfg.Now()).Add(-Retention)
	for start := range t.buckets {
		if start.Before(cutoff) {
			delete(t.buckets, start)
			delete(t.dirty, start)
		}
	}

	puts = make(map[string][]byte, len(t.dirty))
	for start := range t.dirty {
		data, merr := json.Marshal(t.buckets[start])
		if merr != nil {
			return nil, nil, nil, cutoff, fmt.Errorf("stats: marshal bucket: %w", merr)
		}
		puts[bucketKey(start)] = data
		starts = append(starts, start)
	}
	if t.metaDirty {
		if meta, err = t.encodeMeta(); err != nil {
			return nil, nil, nil, cutoff, err
		}
	}
	t.dirty = make(map[time.Time]struct{})
	t.metaDirty = false
	return puts, starts, meta, cutoff, nil
}

// encodeMeta renders the non-bucket state. Callers hold t.mu.
func (t *Tracker) encodeMeta() (map[string]string, error) {
	meta := map[string]string{}
	for key, ts := range map[string]time.Time{
		metaLastPush: t.lastPush, metaLastPull: t.lastPull, metaLastPoll: t.lastPoll,
	} {
		if !ts.IsZero() {
			meta[key] = ts.UTC().Format(time.RFC3339Nano)
		}
	}
	for _, err := range []error{
		putJSON(meta, metaLastError, t.lastErr),
		putJSON(meta, metaBisync, t.bisync),
		putJSON(meta, metaLocal, t.local),
	} {
		if err != nil {
			return nil, err
		}
	}
	return meta, nil
}

// putJSON stores p as JSON under key, skipping nil pointers.
func putJSON[T any](meta map[string]string, key string, p *T) error {
	if p == nil {
		return nil
	}
	data, err := json.Marshal(p)
	if err != nil {
		return fmt.Errorf("stats: marshal %s: %w", key, err)
	}
	meta[key] = string(data)
	return nil
}

// Load restores persisted buckets and meta values. Unparseable entries
// are skipped (a corrupt stats row must never stop the agent). Call once
// at startup, before RecordStart.
func (t *Tracker) Load() error {
	if t == nil || t.cfg.Store == nil {
		return nil
	}
	rows, meta, err := t.cfg.Store.LoadStats()
	if err != nil {
		return fmt.Errorf("stats: load: %w", err)
	}

	t.mu.Lock()
	defer t.mu.Unlock()
	for key, data := range rows {
		start, perr := time.ParseInLocation(keyLayout, key, time.UTC)
		var b bucket
		if perr != nil || json.Unmarshal(data, &b) != nil {
			t.cfg.Log.Warn("skipping unreadable stats bucket", "op", "stats_load", "key", key)
			continue
		}
		t.buckets[start] = &b
	}
	t.lastPush = parseMetaTime(meta[metaLastPush])
	t.lastPull = parseMetaTime(meta[metaLastPull])
	t.lastPoll = parseMetaTime(meta[metaLastPoll])
	t.lastErr = decodeMeta[homedrive.LastError](meta[metaLastError])
	t.bisync = decodeMeta[homedrive.BisyncInfo](meta[metaBisync])
	t.local = decodeMeta[homedrive.LocalInfo](meta[metaLocal])
	return nil
}

func parseMetaTime(s string) time.Time {
	ts, err := time.Parse(time.RFC3339Nano, s)
	if err != nil {
		return time.Time{}
	}
	return ts.UTC()
}

func decodeMeta[T any](s string) *T {
	if s == "" {
		return nil
	}
	var v T
	if err := json.Unmarshal([]byte(s), &v); err != nil {
		return nil
	}
	return &v
}
