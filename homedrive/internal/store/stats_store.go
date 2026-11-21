package store

import (
	"bytes"
	"errors"
	"fmt"

	bolt "go.etcd.io/bbolt"
)

// ErrStatsBucketMissing is returned when the stats or meta bucket does not
// exist (a journal not opened through OpenJournal).
var ErrStatsBucketMissing = errors.New("store: stats bucket missing")

// bucketStats holds the 5-minute statistics buckets written by
// internal/stats. Key: YYYYMMDDHHMM (UTC bucket start); value: compact
// JSON owned by the stats package.
var bucketStats = []byte("stats")

// StatsMetaPrefix prefixes every meta key owned by the stats tracker, so
// LoadStats can fetch them with one prefix scan.
const StatsMetaPrefix = "stats."

// SaveStats atomically (one write transaction) upserts the given stats
// buckets and meta values, then deletes every stats bucket whose key sorts
// before deleteBefore (when non-empty). Meta keys must start with
// StatsMetaPrefix.
func (j *Journal) SaveStats(puts map[string][]byte, deleteBefore string, meta map[string]string) error {
	return j.db.Update(func(tx *bolt.Tx) error {
		sb := tx.Bucket(bucketStats)
		mb := tx.Bucket(bucketMeta)
		if sb == nil || mb == nil {
			return ErrStatsBucketMissing
		}
		for k, v := range puts {
			if err := sb.Put([]byte(k), v); err != nil {
				return fmt.Errorf("store: put stats bucket %q: %w", k, err)
			}
		}
		for k, v := range meta {
			if err := mb.Put([]byte(k), []byte(v)); err != nil {
				return fmt.Errorf("store: put stats meta %q: %w", k, err)
			}
		}
		if deleteBefore == "" {
			return nil
		}
		if err := deleteStatsBefore(sb, []byte(deleteBefore)); err != nil {
			return fmt.Errorf("store: prune stats before %q: %w", deleteBefore, err)
		}
		return nil
	})
}

// deleteStatsBefore removes every key strictly below limit.
func deleteStatsBefore(sb *bolt.Bucket, limit []byte) error {
	c := sb.Cursor()
	for k, _ := c.First(); k != nil && bytes.Compare(k, limit) < 0; k, _ = c.First() {
		if err := c.Delete(); err != nil {
			return err
		}
	}
	return nil
}

// LoadStats returns every stats bucket and every stats meta value.
func (j *Journal) LoadStats() (buckets map[string][]byte, meta map[string]string, err error) {
	buckets = make(map[string][]byte)
	meta = make(map[string]string)
	err = j.db.View(func(tx *bolt.Tx) error {
		sb, mb := tx.Bucket(bucketStats), tx.Bucket(bucketMeta)
		if sb == nil || mb == nil {
			return ErrStatsBucketMissing
		}
		if err := sb.ForEach(func(k, v []byte) error {
			buckets[string(k)] = append([]byte(nil), v...)
			return nil
		}); err != nil {
			return fmt.Errorf("store: read stats buckets: %w", err)
		}
		c := mb.Cursor()
		prefix := []byte(StatsMetaPrefix)
		for k, v := c.Seek(prefix); k != nil && bytes.HasPrefix(k, prefix); k, v = c.Next() {
			meta[string(k)] = string(v)
		}
		return nil
	})
	return buckets, meta, err
}
