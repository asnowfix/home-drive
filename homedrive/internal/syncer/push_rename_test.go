package syncer

import (
	"testing"
	"time"
)

// Issue #155 review M1/R1: a file moved out of the watched tree (what the
// trash rename looks like to inotify) arrives as an unpaired OpRename of the
// old path. It must make no Drive call (a removal could delete a conflict
// winner mid-swap) and must not fail as "unhandled operation".
func TestSyncer_UnpairedRename_NoDriveCallNoFailure(t *testing.T) {
	tests := []struct {
		name        string
		stillThere  bool
		wantDeletes int
	}{
		{"moved_out_old_path_gone_makes_no_drive_call", false, 0},
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
			if len(remote.getCopyCalls()) != 0 {
				t.Errorf("unexpected upload for an unpaired rename")
			}
			for _, m := range pub.getEvents() {
				if m.Topic == "homedrive/test/events/push.failure" {
					t.Errorf("unexpected push.failure event: %+v", m)
				}
			}
		})
	}
}
