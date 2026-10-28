package syncer

import (
	"os"
	"path/filepath"
	"testing"
	"time"
)

func TestSyncer_DirEventNotUploaded(t *testing.T) {
	tests := []struct {
		name     string
		op       Op
		isDir    bool
		wantCopy bool
		wantPuts int
	}{
		{"DirCreate", OpCreate, true, false, 0},
		{"DirWrite", OpWrite, true, false, 0},
		{"FileCreate", OpCreate, false, true, 1},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			root := t.TempDir()
			target := filepath.Join(root, "hd-test")
			if tt.isDir {
				if err := os.Mkdir(target, 0o755); err != nil {
					t.Fatal(err)
				}
			} else if err := os.WriteFile(target, []byte("x"), 0o644); err != nil {
				t.Fatal(err)
			}

			remote := newMockRemoteFS()
			store := newMockStore()
			cfg := DefaultConfig()
			cfg.LocalRoot = root
			s := New(cfg, remote, store, newMockAuditLogger(), newMockPublisher(),
				newDiscardLogger(), WithSleepFunc(noSleep))

			events := make(chan Event, 1)
			events <- Event{Path: "hd-test", Op: tt.op, At: time.Now()}
			runSyncer(t, s, events, make(chan DirRename), func() {})

			if got := len(remote.getCopyCalls()); (got > 0) != tt.wantCopy {
				t.Errorf("CopyFile calls = %d, wantCopy %v", got, tt.wantCopy)
			}
			if got := len(store.getPuts()); got != tt.wantPuts {
				t.Errorf("store puts = %d, want %d", got, tt.wantPuts)
			}
		})
	}
}
