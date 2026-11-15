package store

import (
	"bytes"
	"fmt"

	bolt "go.etcd.io/bbolt"
)

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
			return fmt.Errorf("store: stats bucket missing")
		}
		for k, v := range puts {
			if err := sb.Put([]byte(k), v); err != nil {
				return err
			}
		}
		for k, v := range meta {
			if err := mb.Put([]byte(k), []byte(v)); err != nil {
				return err
			}
		}
		if deleteBefore == "" {
			return nil
		}
		return deleteStatsBefore(sb, []byte(deleteBefore))
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
			return nil
		}
		if err := sb.ForEach(func(k, v []byte) error {
			buckets[string(k)] = append([]byte(nil), v...)
			return nil
		}); err != nil {
			return err
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
