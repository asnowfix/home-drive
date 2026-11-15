package syncer

import (
	"context"
	"errors"
	"path/filepath"
	"testing"
	"time"
)

// TestPullerRecorder_UnrecordedLeafFailures: every final pull failure
// must reach the recorder exactly once, including the leaf errors that
// have no recording of their own (journal read, stat, mkdir, journal put).
func TestPullerRecorder_UnrecordedLeafFailures(t *testing.T) {
	now := time.Date(2026, 11, 15, 12, 0, 0, 0, time.UTC)
	synced := now.Add(-time.Hour)
	notADir := func(t *testing.T, root string) {
		t.Helper()
		if err := writeTestFile(filepath.Join(root, "sub"), "a file, not a dir"); err != nil {
			t.Fatal(err)
		}
	}
	tests := []struct {
		name   string
		path   string
		wantOp string
		setup  func(t *testing.T, root string, st *mockStore)
	}{
		{"journal_read_error", "a.txt", OutPull, func(_ *testing.T, _ string, st *mockStore) {
			st.getErr = errors.New("bolt: read failed")
		}},
		{"detect_conflict_stat_error", "sub/b.txt", OutPull, func(t *testing.T, root string, st *mockStore) {
			notADir(t, root)
			st.entries["sub/b.txt"] = JournalEntry{Path: "sub/b.txt", RemoteMD5: "old", LocalMtime: synced, RemoteMtime: synced}
		}},
		{"mkdir_error", "sub/c.txt", OutPull, func(t *testing.T, root string, _ *mockStore) {
			notADir(t, root)
		}},
		{"conflict_journal_put_error", "d.txt", OutConflict, func(t *testing.T, root string, st *mockStore) {
			writeTestFileWithMtime(t, filepath.Join(root, "d.txt"), "local", now.Add(-time.Minute))
			st.entries["d.txt"] = JournalEntry{Path: "d.txt", RemoteMD5: "old", LocalMtime: synced, RemoteMtime: synced}
			st.putErr = errors.New("bolt: put failed")
		}},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			root := t.TempDir()
			st := newMockStore()
			tc.setup(t, root, st)
			remote := newMockRemoteFS()
			remote.startToken = "t1"
			remote.files[tc.path] = RemoteObject{Path: tc.path, MD5: "new", ModTime: now, RemoteID: "id"}
			remote.changes["t1"] = Changes{
				Items: []Change{{Path: tc.path, Object: &RemoteObject{
					Path: tc.path, Size: 5, MD5: "new", ModTime: now, RemoteID: "id",
				}}},
				NextPageToken: "t2",
			}
			rec := &fakeRecorder{}
			p := NewPuller(PullerConfig{Interval: time.Second, LocalRoot: root, Recorder: rec},
				remote, st, newMockAuditLogger(), newMockPublisher(), newDiscardLogger(), fixedClock(now))
			if err := p.PollOnce(context.Background()); err == nil {
				t.Fatal("expected PollOnce to return the failure")
			}
			var fails []Outcome
			for _, o := range rec.get() {
				if o.Err != nil && o.Op != OutPoll {
					fails = append(fails, o)
				}
			}
			if len(fails) != 1 || fails[0].Op != tc.wantOp {
				t.Fatalf("failure outcomes = %+v, want exactly one %s", fails, tc.wantOp)
			}
		})
	}
}

// TestPullerRecorder_PollNotSuccessfulUntilTokenPersisted: a poll whose
// page token cannot be persisted must not advance last_poll.
func TestPullerRecorder_PollNotSuccessfulUntilTokenPersisted(t *testing.T) {
	tests := []struct {
		name  string
		items []Change
	}{
		{"zero_changes", nil},
		{"with_changes", []Change{{Path: "gone.txt", Deleted: true}}},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			remote := newMockRemoteFS()
			remote.startToken = "t1"
			remote.changes["t1"] = Changes{Items: tc.items, NextPageToken: "t2"}
			st := newMockStore()
			st.pageToken = "t1"
			st.setTokenErr = errors.New("bolt: disk full")
			rec := &fakeRecorder{}
			p := NewPuller(PullerConfig{Interval: time.Second, LocalRoot: t.TempDir(), Recorder: rec},
				remote, st, newMockAuditLogger(), newMockPublisher(), newDiscardLogger(),
				fixedClock(time.Now()))
			if err := p.PollOnce(context.Background()); err == nil {
				t.Fatal("expected persist error")
			}
			polls := rec.byOp(OutPoll)
			if len(polls) != 1 || polls[0].Err == nil {
				t.Fatalf("poll outcomes = %+v, want a single failure", polls)
			}
		})
	}
}
