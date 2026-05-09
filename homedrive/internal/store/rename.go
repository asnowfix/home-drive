package store

import (
	"encoding/json"
	"fmt"
	"log/slog"
	"strings"

	bolt "go.etcd.io/bbolt"
)

// RewritePrefix rewrites all journal entries whose path starts with
// oldPrefix to use newPrefix instead. This is executed in a single Bolt
// transaction for atomicity. It returns the number of entries rewritten.
//
// This supports the directory rename optimization described in PLAN.md 6.4:
// a single Bolt TX rewrites all keys with prefix "from/" to prefix "to/".
func RewritePrefix(j *Journal, oldPrefix, newPrefix string, auditor *Auditor, logger *slog.Logger) (int, error) {
	if oldPrefix == newPrefix {
		return 0, nil
	}

	oldPrefixBytes := []byte(oldPrefix)
	count := 0

	err := j.db.Update(func(tx *bolt.Tx) error {
		b := tx.Bucket(bucketJournal)
		c := b.Cursor()

		// Collect entries to rewrite. We must not modify the bucket
		// while iterating with a cursor, so we buffer the operations.
		type rewriteOp struct {
			oldKey []byte
			entry  JournalEntry
		}
		var ops []rewriteOp

		for k, v := c.Seek(oldPrefixBytes); k != nil && hasPrefix(k, oldPrefixBytes); k, v = c.Next() {
			var entry JournalEntry
			if err := json.Unmarshal(v, &entry); err != nil {
				return fmt.Errorf("store: unmarshal %q during rename: %w", string(k), err)
			}

			// Rewrite the path: replace oldPrefix with newPrefix.
			entry.Path = newPrefix + strings.TrimPrefix(entry.Path, oldPrefix)

			// Copy the key since cursor data is only valid for the
			// duration of the transaction.
			keyCopy := make([]byte, len(k))
			copy(keyCopy, k)

			ops = append(ops, rewriteOp{oldKey: keyCopy, entry: entry})
		}

		// Apply all rewrites in the same transaction.
		for _, op := range ops {
			// Delete old key.
			if err := b.Delete(op.oldKey); err != nil {
				return fmt.Errorf("store: delete old key %q: %w", string(op.oldKey), err)
			}

			// Put new key.
			data, err := json.Marshal(op.entry)
			if err != nil {
				return fmt.Errorf("store: marshal entry for %q: %w", op.entry.Path, err)
			}
			if err := b.Put([]byte(op.entry.Path), data); err != nil {
				return fmt.Errorf("store: put new key %q: %w", op.entry.Path, err)
			}
			count++
		}

		return nil
	})

	if err != nil {
		return 0, err
	}

	logger.Info("prefix rewrite complete",
		"op", "dir_rename",
		"old_prefix", oldPrefix,
		"new_prefix", newPrefix,
		"files_count", count,
	)

	if auditor != nil {
		auditor.Log(AuditEntry{
			Op:         "dir_rename",
			Path:       oldPrefix,
			NewPath:    newPrefix,
			FilesCount: count,
		})
	}

	return count, nil
}
