package store

import (
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"testing"
	"time"
)

func TestJournal_RemoteIDPaths(t *testing.T) {
	tests := []struct {
		name    string
		entries []JournalEntry
		want    map[string][]string
	}{
		{"empty journal", nil, map[string][]string{}},
		{
			"entries without ID are skipped",
			[]JournalEntry{{Path: "old.txt"}, {Path: "new.txt", RemoteID: "id-1"}},
			map[string][]string{"id-1": {"new.txt"}},
		},
		{
			"shared ID lists every path",
			[]JournalEntry{
				{Path: "a.txt", RemoteID: "dup"},
				{Path: "b.txt", RemoteID: "dup"},
				{Path: "c.txt", RemoteID: "id-3"},
			},
			map[string][]string{"dup": {"a.txt", "b.txt"}, "id-3": {"c.txt"}},
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			j := newTestJournal(t)
			for _, e := range tc.entries {
				if err := j.Put(e); err != nil {
					t.Fatalf("Put: %v", err)
				}
			}
			got, err := j.RemoteIDPaths()
			if err != nil {
				t.Fatalf("RemoteIDPaths: %v", err)
			}
			if len(got) != len(tc.want) {
				t.Fatalf("got %v, want %v", got, tc.want)
			}
			for id, ps := range tc.want {
				if len(got[id]) != len(ps) {
					t.Fatalf("id %s: got %v, want %v", id, got[id], ps)
				}
				for i := range ps {
					if got[id][i] != ps[i] {
						t.Errorf("id %s: got %v, want %v", id, got[id], ps)
					}
				}
			}
		})
	}
}

// BenchmarkJournal_RemoteIDPaths100k measures the per-poll cost of the ID
// scan against a 100k-entry journal (issue #163).
func BenchmarkJournal_RemoteIDPaths100k(b *testing.B) {
	logger := slog.New(slog.NewJSONHandler(os.Stderr, nil))
	j, err := OpenJournal(filepath.Join(b.TempDir(), "bench.db"), logger)
	if err != nil {
		b.Fatal(err)
	}
	defer func() { _ = j.Close() }()
	now := time.Now()
	for i := 0; i < 100000; i++ {
		e := JournalEntry{
			Path:         fmt.Sprintf("Pictures/2026/album-%04d/photo-%06d.jpg", i%1000, i),
			LocalMtime:   now,
			RemoteMtime:  now,
			RemoteMD5:    "d41d8cd98f00b204e9800998ecf8427e",
			RemoteID:     fmt.Sprintf("1AbCdEfGhIjKlMnOpQrStUvWxYz%06d", i),
			LastSyncedAt: now,
			LastOrigin:   "remote",
		}
		if err := j.Put(e); err != nil {
			b.Fatal(err)
		}
	}
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		if _, err := j.RemoteIDPaths(); err != nil {
			b.Fatal(err)
		}
	}
}
