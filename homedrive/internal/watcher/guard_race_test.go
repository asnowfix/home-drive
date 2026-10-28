package watcher

import (
	"log/slog"
	"os"
	"path/filepath"
	"testing"
	"time"
)

// TestWatcher_GuardAtFireTime reproduces #122: the sync record is written
// after the inotify event arrives (as the Puller does, after DownloadFile)
// but before the debounce window elapses.
func TestWatcher_GuardAtFireTime(t *testing.T) {
	tests := []struct {
		name string
		// record builds the record to store from the file's final stat;
		// nil means no record is ever stored.
		record      func(info os.FileInfo) *SyncRecord
		wantEmitted bool
	}{
		{
			name: "record_matches_suppressed",
			record: func(i os.FileInfo) *SyncRecord {
				return &SyncRecord{LocalMtime: i.ModTime(), Size: i.Size()}
			},
			wantEmitted: false,
		},
		{
			name: "record_mtime_differs_emitted",
			record: func(i os.FileInfo) *SyncRecord {
				return &SyncRecord{LocalMtime: i.ModTime().Add(-5 * time.Second), Size: i.Size()}
			},
			wantEmitted: true,
		},
		{
			name: "record_size_differs_emitted",
			record: func(i os.FileInfo) *SyncRecord {
				return &SyncRecord{LocalMtime: i.ModTime(), Size: i.Size() + 1}
			},
			wantEmitted: true,
		},
		{
			name:        "no_record_emitted",
			record:      nil,
			wantEmitted: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			root := t.TempDir()
			store := &stubStore{records: map[string]*SyncRecord{}}
			cfg := Config{
				LocalRoot:           root,
				Debounce:            300 * time.Millisecond,
				DirRenamePairWindow: 200 * time.Millisecond,
			}
			log := slog.New(slog.NewJSONHandler(os.Stderr, nil))
			w, err := New(cfg, store, log)
			if err != nil {
				t.Fatalf("New: %v", err)
			}
			startWatcher(t, w)

			path := filepath.Join(root, "pulled.txt")
			if err := os.WriteFile(path, []byte("data"), 0o644); err != nil {
				t.Fatalf("write: %v", err)
			}
			// Event has arrived (no record yet); record lands inside the window.
			time.Sleep(100 * time.Millisecond)
			info, err := os.Stat(path)
			if err != nil {
				t.Fatalf("stat: %v", err)
			}
			if tt.record != nil {
				store.set(path, tt.record(info))
			}

			emitted := false
			for _, ev := range collectEvents(t, w, 800*time.Millisecond) {
				if ev.Event != nil && ev.Event.Path == path {
					emitted = true
				}
			}
			if emitted != tt.wantEmitted {
				t.Errorf("emitted = %v, want %v", emitted, tt.wantEmitted)
			}
		})
	}
}
