package stats

import (
	"context"
	"errors"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/asnowfix/home-drive/homedrive/internal/store"
	"github.com/asnowfix/home-drive/homedrive/internal/syncer"
)

var t0 = time.Date(2026, 11, 15, 12, 7, 0, 0, time.UTC)

func newJournal(t *testing.T, path string) *store.Journal {
	t.Helper()
	j, err := store.OpenJournal(path, slog.New(slog.NewJSONHandler(os.Stderr, nil)))
	if err != nil {
		t.Fatalf("OpenJournal: %v", err)
	}
	return j
}

func fixedNow(ts time.Time) func() time.Time { return func() time.Time { return ts } }

func TestTracker_WindowEdges(t *testing.T) {
	// now = 12:07; bucket starts are 5-minute aligned.
	tests := []struct {
		name      string
		at        time.Time
		want1h    int64
		want24h   int64
		want7d    int64
		wantBytes int64
	}{
		{"current_bucket", t0, 1, 1, 1, 10},
		{"1h_edge_included", t0.Add(-time.Hour).Truncate(BucketSize).Add(BucketSize), 1, 1, 1, 10},
		{"just_outside_1h", t0.Add(-time.Hour).Truncate(BucketSize), 0, 1, 1, 10},
		{"24h_edge_included", t0.Add(-24 * time.Hour).Truncate(BucketSize).Add(BucketSize), 0, 1, 1, 10},
		{"just_outside_24h", t0.Add(-24 * time.Hour).Truncate(BucketSize), 0, 0, 1, 10},
		{"just_outside_7d", t0.Add(-7 * 24 * time.Hour).Truncate(BucketSize), 0, 0, 0, 10},
		{"future_ignored", t0.Add(time.Hour), 0, 0, 0, 10},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			tr := New(Config{Now: fixedNow(t0)})
			tr.Record(syncer.Outcome{Op: syncer.OutPull, Dir: syncer.DirDown, Bytes: tc.wantBytes, At: tc.at})
			c := tr.Snapshot(t0).Counts
			if c["1h"].FilesDown != tc.want1h || c["24h"].FilesDown != tc.want24h || c["7d"].FilesDown != tc.want7d {
				t.Errorf("files_down 1h/24h/7d = %d/%d/%d, want %d/%d/%d",
					c["1h"].FilesDown, c["24h"].FilesDown, c["7d"].FilesDown, tc.want1h, tc.want24h, tc.want7d)
			}
		})
	}
}

func TestTracker_RecordSemantics(t *testing.T) {
	boom := errors.New("boom")
	tests := []struct {
		name string
		out  syncer.Outcome
		want func(s Snapshot) bool
	}{
		{"upload_counts_bytes_and_last_push",
			syncer.Outcome{Op: syncer.OutPush, Dir: syncer.DirUp, Bytes: 7, At: t0},
			func(s Snapshot) bool {
				c := s.Counts["24h"]
				return c.FilesUp == 1 && c.BytesUp == 7 && s.LastPush.Equal(t0) && s.LastPull.IsZero()
			}},
		{"download_counts_bytes_and_last_pull",
			syncer.Outcome{Op: syncer.OutPull, Dir: syncer.DirDown, Bytes: 9, At: t0},
			func(s Snapshot) bool {
				c := s.Counts["24h"]
				return c.FilesDown == 1 && c.BytesDown == 9 && s.LastPull.Equal(t0) && s.LastActivity.Equal(t0)
			}},
		{"delete_moves_last_push_only",
			syncer.Outcome{Op: syncer.OutPushDelete, At: t0},
			func(s Snapshot) bool { return s.Counts["24h"].FilesUp == 0 && s.LastPush.Equal(t0) }},
		{"poll_sets_last_poll_not_last_pull",
			syncer.Outcome{Op: syncer.OutPoll, At: t0},
			func(s Snapshot) bool { return s.LastPoll.Equal(t0) && s.LastPull.IsZero() }},
		{"push_error_counts_up",
			syncer.Outcome{Op: syncer.OutPush, Dir: syncer.DirUp, Err: boom, At: t0},
			func(s Snapshot) bool {
				c := s.Counts["1h"]
				return c.Errors == 1 && c.ErrorsUp == 1 && c.ErrorsDown == 0 && c.FilesUp == 0 &&
					s.LastError != nil && s.LastError.Op == "push" && s.LastError.Message == "boom" &&
					s.LastPush.IsZero()
			}},
		{"poll_error_counts_down",
			syncer.Outcome{Op: syncer.OutPoll, Err: boom, At: t0},
			func(s Snapshot) bool {
				c := s.Counts["1h"]
				return c.Errors == 1 && c.ErrorsDown == 1 && c.ErrorsUp == 0 && s.LastPoll.IsZero()
			}},
		{"conflict_counts",
			syncer.Outcome{Op: syncer.OutConflict, At: t0},
			func(s Snapshot) bool { return s.Counts["24h"].Conflicts == 1 }},
		{"conflict_failure_counts_error_no_direction",
			syncer.Outcome{Op: syncer.OutConflict, Err: boom, At: t0},
			func(s Snapshot) bool {
				c := s.Counts["24h"]
				return c.Conflicts == 1 && c.Errors == 1 && c.ErrorsUp == 0 && c.ErrorsDown == 0
			}},
		{"canceled_ignored",
			syncer.Outcome{Op: syncer.OutPush, Err: context.Canceled, At: t0},
			func(s Snapshot) bool { return s.Counts["24h"].Errors == 0 && s.LastError == nil }},
		{"dangling_ignored",
			syncer.Outcome{Op: syncer.OutPull, Err: syncer.ErrDanglingShortcut, At: t0},
			func(s Snapshot) bool { return s.Counts["24h"].Errors == 0 }},
		{"bisync_pass_ok",
			syncer.Outcome{Op: syncer.OutBisync, Files: 3, Duration: 1500 * time.Millisecond, At: t0},
			func(s Snapshot) bool {
				return s.Bisync != nil && s.Bisync.Result == "ok" && s.Bisync.FilesChanged == 3 && s.Bisync.DurationMs == 1500
			}},
		{"bisync_pass_error",
			syncer.Outcome{Op: syncer.OutBisync, Err: boom, At: t0},
			func(s Snapshot) bool {
				return s.Bisync != nil && s.Bisync.Result == "error" && s.Bisync.Error == "boom" &&
					s.Counts["1h"].Errors == 1
			}},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			tr := New(Config{Now: fixedNow(t0)})
			tr.Record(tc.out)
			if s := tr.Snapshot(t0); !tc.want(s) {
				t.Errorf("unexpected snapshot: %+v", s)
			}
		})
	}
}

func TestTracker_LastErrorTruncation(t *testing.T) {
	tests := []struct {
		name string
		msg  string
	}{
		{"short", "short"},
		{"long_ascii", strings.Repeat("a", 1000)},
		{"long_multibyte", strings.Repeat("é", 300)},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			tr := New(Config{Now: fixedNow(t0)})
			tr.Record(syncer.Outcome{Op: syncer.OutPush, Dir: syncer.DirUp, Err: errors.New(tc.msg), At: t0})
			got := tr.Snapshot(t0).LastError.Message
			if len(got) > MaxErrorMessage {
				t.Errorf("len = %d, want <= %d", len(got), MaxErrorMessage)
			}
			if !strings.HasPrefix(tc.msg, got) {
				t.Errorf("truncated message is not a prefix of the original")
			}
		})
	}
}

// TestTracker_FlushLoadRoundTrip is the core assertion for bug (c): the
// counters and last-* values survive a restart.
func TestTracker_FlushLoadRoundTrip(t *testing.T) {
	path := filepath.Join(t.TempDir(), "state.db")
	j := newJournal(t, path)
	tr := New(Config{Store: j, Now: fixedNow(t0)})
	tr.Record(syncer.Outcome{Op: syncer.OutPull, Dir: syncer.DirDown, Bytes: 20 << 20, At: t0})
	tr.Record(syncer.Outcome{Op: syncer.OutPush, Dir: syncer.DirUp, Err: errors.New("denied"), At: t0})
	tr.Record(syncer.Outcome{Op: syncer.OutPoll, At: t0})
	tr.Record(syncer.Outcome{Op: syncer.OutBisync, Files: 2, At: t0})
	tr.RecordLocalTree(12, 3456, t0)
	tr.RecordStart()
	if err := tr.Flush(); err != nil {
		t.Fatalf("Flush: %v", err)
	}
	if err := j.Close(); err != nil {
		t.Fatal(err)
	}

	// "Restart": reopen the same file with a fresh tracker.
	j2 := newJournal(t, path)
	defer func() { _ = j2.Close() }()
	tr2 := New(Config{Store: j2, Now: fixedNow(t0.Add(time.Minute))})
	if err := tr2.Load(); err != nil {
		t.Fatalf("Load: %v", err)
	}
	tr2.RecordStart()

	s := tr2.Snapshot(t0.Add(time.Minute))
	c := s.Counts["24h"]
	if c.BytesDown != 20<<20 || c.FilesDown != 1 || c.Errors != 1 || c.ErrorsUp != 1 || c.Starts != 2 {
		t.Errorf("24h counts after restart = %+v", c)
	}
	if !s.LastPull.Equal(t0) || !s.LastPoll.Equal(t0) || !s.LastPush.IsZero() {
		t.Errorf("last_* after restart = push %v pull %v poll %v", s.LastPush, s.LastPull, s.LastPoll)
	}
	if s.LastError == nil || s.LastError.Message != "denied" {
		t.Errorf("last_error = %+v", s.LastError)
	}
	if s.Bisync == nil || s.Bisync.FilesChanged != 2 {
		t.Errorf("bisync = %+v", s.Bisync)
	}
	if s.Local == nil || s.Local.Files != 12 || s.Local.Bytes != 3456 {
		t.Errorf("local = %+v", s.Local)
	}
}

func TestTracker_FlushPrunesPastRetention(t *testing.T) {
	j := newJournal(t, filepath.Join(t.TempDir(), "state.db"))
	defer func() { _ = j.Close() }()

	old := t0.Add(-Retention - 2*BucketSize)
	if err := j.SaveStats(map[string][]byte{bucketKey(bucketStart(old)): []byte(`{"fd":1}`)}, "", nil); err != nil {
		t.Fatal(err)
	}
	tr := New(Config{Store: j, Now: fixedNow(t0)})
	if err := tr.Load(); err != nil {
		t.Fatal(err)
	}
	tr.Record(syncer.Outcome{Op: syncer.OutPull, Dir: syncer.DirDown, At: t0})
	if err := tr.Flush(); err != nil {
		t.Fatalf("Flush: %v", err)
	}

	rows, _, err := j.LoadStats()
	if err != nil {
		t.Fatal(err)
	}
	if len(rows) != 1 {
		t.Errorf("rows after prune = %d, want 1 (old bucket deleted)", len(rows))
	}
}

func TestTracker_LoadSkipsCorruptRows(t *testing.T) {
	j := newJournal(t, filepath.Join(t.TempDir(), "state.db"))
	defer func() { _ = j.Close() }()
	good := bucketKey(bucketStart(t0))
	if err := j.SaveStats(map[string][]byte{
		good: []byte(`{"fd":3}`), "notatime": []byte(`{}`), "202611150000": []byte(`not json`),
	}, "", map[string]string{"stats.last.error": "garbage"}); err != nil {
		t.Fatal(err)
	}
	tr := New(Config{Store: j, Now: fixedNow(t0)})
	if err := tr.Load(); err != nil {
		t.Fatalf("Load: %v", err)
	}
	s := tr.Snapshot(t0)
	if s.Counts["1h"].FilesDown != 3 || s.LastError != nil {
		t.Errorf("snapshot = %+v", s)
	}
}

type failingStore struct{ fail bool }

func (f *failingStore) SaveStats(map[string][]byte, string, map[string]string) error {
	if f.fail {
		return errors.New("disk full")
	}
	return nil
}
func (f *failingStore) LoadStats() (map[string][]byte, map[string]string, error) {
	return nil, nil, nil
}

func TestTracker_FlushFailureKeepsDirty(t *testing.T) {
	fs := &failingStore{fail: true}
	tr := New(Config{Store: fs, Now: fixedNow(t0)})
	tr.Record(syncer.Outcome{Op: syncer.OutPull, Dir: syncer.DirDown, At: t0})
	if err := tr.Flush(); err == nil {
		t.Fatal("expected flush error")
	}
	fs.fail = false
	if err := tr.Flush(); err != nil {
		t.Fatalf("retry Flush: %v", err)
	}
	if len(tr.dirty) != 0 {
		t.Error("dirty set should be empty after a successful flush")
	}
	if tr.Snapshot(t0).Counts["1h"].FilesDown != 1 {
		t.Error("counts must be kept in memory across a failed flush")
	}
}

func TestTracker_NilIsSafe(t *testing.T) {
	var tr *Tracker
	tr.Record(syncer.Outcome{Op: syncer.OutPush})
	tr.RecordLocalTree(1, 1, t0)
	tr.RecordStart()
	if err := tr.Flush(); err != nil {
		t.Error(err)
	}
	if err := tr.Load(); err != nil {
		t.Error(err)
	}
	if got := tr.Snapshot(t0).Counts["24h"]; got.FilesUp != 0 {
		t.Errorf("nil tracker counts = %+v", got)
	}
}
