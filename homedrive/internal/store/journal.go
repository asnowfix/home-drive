// Package store manages the local BoltDB journal tracking sync state for
// every file, the .old.<N> conflict index, and the JSONL audit log.
package store

import (
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"time"

	bolt "go.etcd.io/bbolt"
)

// Bucket names used inside the BoltDB file.
var (
	bucketJournal = []byte("journal")
	bucketMeta    = []byte("meta")
)

var keyPageToken = []byte("page_token")

// Sentinel errors for the store package.
var (
	ErrNotFound = errors.New("store: entry not found")
	ErrDBClosed = errors.New("store: database is closed")
)

// JournalEntry records the last known sync state for a single path.
type JournalEntry struct {
	Path         string    `json:"path"`
	LocalMtime   time.Time `json:"local_mtime"`
	RemoteMtime  time.Time `json:"remote_mtime"`
	RemoteMD5    string    `json:"remote_md5"`
	RemoteID     string    `json:"remote_id"`
	LastSyncedAt time.Time `json:"last_synced_at"`
	LastOrigin   string    `json:"last_origin"` // "local" | "remote"
}

// Journal is a BoltDB-backed store indexed by file path.
type Journal struct {
	db     *bolt.DB
	logger *slog.Logger
}

// OpenJournal opens (or creates) a BoltDB database at the given path and
// ensures the required buckets exist.
func OpenJournal(path string, logger *slog.Logger) (*Journal, error) {
	db, err := bolt.Open(path, 0600, &bolt.Options{Timeout: 1 * time.Second})
	if err != nil {
		return nil, fmt.Errorf("store: open db %q: %w", path, err)
	}

	if err := db.Update(func(tx *bolt.Tx) error {
		if _, err := tx.CreateBucketIfNotExists(bucketJournal); err != nil {
			return err
		}
		_, err := tx.CreateBucketIfNotExists(bucketMeta)
		return err
	}); err != nil {
		_ = db.Close()
		return nil, fmt.Errorf("store: create bucket: %w", err)
	}

	return &Journal{db: db, logger: logger}, nil
}

// Close closes the underlying BoltDB database.
func (j *Journal) Close() error {
	if j.db == nil {
		return ErrDBClosed
	}
	return j.db.Close()
}

// Put writes or overwrites a journal entry for the given path.
func (j *Journal) Put(entry JournalEntry) error {
	data, err := json.Marshal(entry)
	if err != nil {
		return fmt.Errorf("store: marshal entry for %q: %w", entry.Path, err)
	}

	return j.db.Update(func(tx *bolt.Tx) error {
		return tx.Bucket(bucketJournal).Put([]byte(entry.Path), data)
	})
}

// Get retrieves the journal entry for the given path.
// Returns ErrNotFound if no entry exists.
func (j *Journal) Get(path string) (JournalEntry, error) {
	var entry JournalEntry

	err := j.db.View(func(tx *bolt.Tx) error {
		data := tx.Bucket(bucketJournal).Get([]byte(path))
		if data == nil {
			return ErrNotFound
		}
		return json.Unmarshal(data, &entry)
	})

	return entry, err
}

// Exists returns true if the path has an entry in the journal.
func (j *Journal) Exists(path string) bool {
	found := false
	_ = j.db.View(func(tx *bolt.Tx) error {
		found = tx.Bucket(bucketJournal).Get([]byte(path)) != nil
		return nil
	})
	return found
}

// Delete removes the journal entry for the given path.
func (j *Journal) Delete(path string) error {
	return j.db.Update(func(tx *bolt.Tx) error {
		return tx.Bucket(bucketJournal).Delete([]byte(path))
	})
}

// ListByPrefix returns all journal entries whose path starts with the
// given prefix. This is used for directory operations.
func (j *Journal) ListByPrefix(prefix string) ([]JournalEntry, error) {
	var entries []JournalEntry
	prefixBytes := []byte(prefix)

	err := j.db.View(func(tx *bolt.Tx) error {
		c := tx.Bucket(bucketJournal).Cursor()
		for k, v := c.Seek(prefixBytes); k != nil && hasPrefix(k, prefixBytes); k, v = c.Next() {
			var entry JournalEntry
			if err := json.Unmarshal(v, &entry); err != nil {
				return fmt.Errorf("store: unmarshal entry for %q: %w", string(k), err)
			}
			entries = append(entries, entry)
		}
		return nil
	})

	return entries, err
}

// Count returns the total number of entries in the journal.
func (j *Journal) Count() (int, error) {
	var count int
	err := j.db.View(func(tx *bolt.Tx) error {
		count = tx.Bucket(bucketJournal).Stats().KeyN
		return nil
	})
	return count, err
}

// DB returns the underlying bolt.DB for use by other store functions
// that need transactional access (e.g. bulk rename).
func (j *Journal) DB() *bolt.DB {
	return j.db
}

// GetPageToken retrieves the persisted Drive Changes API page token.
// Returns an empty string if no token has been stored yet.
func (j *Journal) GetPageToken() (string, error) {
	var token string
	err := j.db.View(func(tx *bolt.Tx) error {
		b := tx.Bucket(bucketMeta)
		if b == nil {
			return nil
		}
		if v := b.Get(keyPageToken); v != nil {
			token = string(v)
		}
		return nil
	})
	return token, err
}

// SetPageToken persists the Drive Changes API page token.
func (j *Journal) SetPageToken(token string) error {
	return j.db.Update(func(tx *bolt.Tx) error {
		b := tx.Bucket(bucketMeta)
		if b == nil {
			return fmt.Errorf("store: meta bucket missing")
		}
		return b.Put(keyPageToken, []byte(token))
	})
}

// NextOldN returns the smallest positive N such that "<path>.old.<N>"
// has no journal entry, used when computing conflict loser filenames.
func (j *Journal) NextOldN(path string) int {
	n := 1
	for {
		if !j.Exists(fmt.Sprintf("%s.old.%d", path, n)) {
			return n
		}
		n++
	}
}

// hasPrefix checks whether key starts with prefix (byte-level comparison).
func hasPrefix(key, prefix []byte) bool {
	if len(key) < len(prefix) {
		return false
	}
	for i, b := range prefix {
		if key[i] != b {
			return false
		}
	}
	return true
}
