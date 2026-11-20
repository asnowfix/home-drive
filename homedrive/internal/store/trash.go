package store

import (
	"encoding/json"
	"fmt"
	"time"

	bolt "go.etcd.io/bbolt"
)

var bucketTrash = []byte("trash")

// TrashRecord remembers one entry that homedrive moved to a
// freedesktop.org Trash (issue #155). Only recorded entries are ever purged:
// everything else in a trash belongs to the user or another program.
type TrashRecord struct {
	Dir       string    `json:"dir"`        // absolute trash directory
	Name      string    `json:"name"`       // entry name under files/ and info/
	Path      string    `json:"path"`       // original absolute path
	DeletedAt time.Time `json:"deleted_at"` // the deletion instant, in UTC
	// Date is the DeletionDate string exactly as written to .trashinfo
	// (local time, no zone). Matching compares it verbatim so a timezone or
	// DST change cannot make a still-valid entry look foreign.
	Date string `json:"date,omitempty"`
}

func trashKey(dir, name string) []byte { return []byte(dir + "\x00" + name) }

// PutTrashRecord stores (or overwrites) r.
func (j *Journal) PutTrashRecord(r TrashRecord) error {
	data, err := json.Marshal(r)
	if err != nil {
		return fmt.Errorf("store: marshal trash record: %w", err)
	}
	return j.db.Update(func(tx *bolt.Tx) error {
		return tx.Bucket(bucketTrash).Put(trashKey(r.Dir, r.Name), data)
	})
}

// DeleteTrashRecord forgets the record for (dir, name).
func (j *Journal) DeleteTrashRecord(dir, name string) error {
	return j.db.Update(func(tx *bolt.Tx) error {
		return tx.Bucket(bucketTrash).Delete(trashKey(dir, name))
	})
}

// ListTrashRecords returns every trash record.
func (j *Journal) ListTrashRecords() ([]TrashRecord, error) {
	var out []TrashRecord
	err := j.db.View(func(tx *bolt.Tx) error {
		return tx.Bucket(bucketTrash).ForEach(func(_, v []byte) error {
			var r TrashRecord
			if err := json.Unmarshal(v, &r); err != nil {
				return fmt.Errorf("store: decode trash record: %w", err)
			}
			out = append(out, r)
			return nil
		})
	})
	return out, err
}
