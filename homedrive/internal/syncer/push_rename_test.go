package syncer

import (
	"testing"
	"time"
)

// Issue #155 review M1: a file moved out of the watched tree (which is what
// the trash rename looks like to inotify) arrives as an unpaired OpRename of
// the old path. It must act as a removal, not fail as "unhandled operation".
func TestSyncer_UnpairedRename_ActsAsRemove(t *testing.T) {
	tests := []struct {
		name        string
		stillThere  bool
		wantDeletes int
	}{
		{"moved_out_old_path_gone_is_removed", false, 1},
		{"old_path_exists_again_is_ignored", true, 0},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			root := t.TempDir()
			if tt.stillThere {
				createLocalFile(t, root, "docs/f.txt", deleteT0)
			}
			remote := newMockRemoteFS()
			pub := newMockPublisher()
			cfg := DefaultConfig()
			cfg.LocalRoot = root
			s := New(cfg, remote, newMockStore(), newMockAuditLogger(),
				pub, newDiscardLogger(), WithSleepFunc(noSleep))

			events := make(chan Event, 1)
			events <- Event{Path: "docs/f.txt", Op: OpRename, At: time.Now()}
			runSyncer(t, s, events, make(chan DirRename), func() {})

			if got := len(remote.getDeleteCalls()); got != tt.wantDeletes {
				t.Errorf("DeleteFile calls = %d, want %d", got, tt.wantDeletes)
			}
			for _, m := range pub.getEvents() {
				if m.Topic == "homedrive/test/events/push.failure" {
					t.Errorf("unexpected push.failure event: %+v", m)
				}
			}
		})
	}
}
