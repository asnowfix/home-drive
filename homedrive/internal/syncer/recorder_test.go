package syncer

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"regexp"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

// fakeRecorder collects outcomes for assertions.
type fakeRecorder struct {
	mu       sync.Mutex
	outcomes []Outcome
	files    int
	bytes    int64
}

func (r *fakeRecorder) Record(o Outcome) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.outcomes = append(r.outcomes, o)
}

func (r *fakeRecorder) RecordLocalTree(files int, bytes int64, _ time.Time) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.files, r.bytes = files, bytes
}

func (r *fakeRecorder) get() []Outcome {
	r.mu.Lock()
	defer r.mu.Unlock()
	return append([]Outcome(nil), r.outcomes...)
}

func (r *fakeRecorder) byOp(op string) []Outcome {
	var out []Outcome
	for _, o := range r.get() {
		if o.Op == op {
			out = append(out, o)
		}
	}
	return out
}

func TestSyncerRecorder_Outcomes(t *testing.T) {
	tests := []struct {
		name     string
		op       Op
		copyFail int32 // number of leading CopyFile failures (-1 = always)
		dryRun   bool
		wantN    int
		wantOp   string
		wantErr  bool
	}{
		{"upload_ok", OpCreate, 0, false, 1, OutPush, false},
		{"flaky_two_failures_then_success", OpWrite, 2, false, 1, OutPush, false},
		{"retries_exhausted", OpCreate, -1, false, 1, OutPush, true},
		{"delete_ok", OpRemove, 0, false, 1, OutPushDelete, false},
		{"dry_run_records_nothing", OpCreate, 0, true, 0, "", false},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			remote := newMockRemoteFS()
			var calls atomic.Int32
			remote.copyErr = func(string) error {
				n := calls.Add(1)
				if tc.copyFail < 0 || n <= tc.copyFail {
					return fmt.Errorf("transient")
				}
				return nil
			}
			rec := &fakeRecorder{}
			cfg := DefaultConfig()
			cfg.DryRun = tc.dryRun
			cfg.Retry.MaxAttempts = 3
			s := New(cfg, remote, newMockStore(), newMockAuditLogger(), newMockPublisher(),
				newDiscardLogger(), WithSleepFunc(noSleep), WithRecorder(rec))

			events := make(chan Event, 1)
			events <- Event{Path: "d/f.txt", Op: tc.op, At: time.Now()}
			runSyncer(t, s, events, make(chan DirRename), func() {})

			got := rec.get()
			if len(got) != tc.wantN {
				t.Fatalf("outcomes = %d (%+v), want %d", len(got), got, tc.wantN)
			}
			if tc.wantN == 0 {
				return
			}
			if got[0].Op != tc.wantOp || (got[0].Err != nil) != tc.wantErr {
				t.Errorf("outcome = %+v, want op %s err=%v", got[0], tc.wantOp, tc.wantErr)
			}
			if tc.wantOp == OutPush && !tc.wantErr {
				if got[0].Dir != DirUp || got[0].Bytes != 100 {
					t.Errorf("upload outcome = %+v, want Dir up Bytes 100", got[0])
				}
			}
		})
	}
}

func TestSyncerRecorder_DirRename(t *testing.T) {
	remote := newMockRemoteFS()
	rec := &fakeRecorder{}
	s := New(DefaultConfig(), remote, newMockStore(), nil, newMockPublisher(),
		newDiscardLogger(), WithSleepFunc(noSleep), WithRecorder(rec))
	dr := make(chan DirRename, 1)
	dr <- DirRename{From: "a", To: "b", At: time.Now()}
	runSyncer(t, s, make(chan Event), dr, func() {})

	got := rec.byOp(OutDirRename)
	if len(got) != 1 || got[0].Err != nil || got[0].Dir != "" {
		t.Fatalf("dir rename outcomes = %+v", got)
	}
}

func TestSyncer_PushEventTimestampIsUTC(t *testing.T) {
	re := regexp.MustCompile(`^\d{4}-\d\d-\d\dT\d\d:\d\d:\d\dZ$`)
	cet := time.FixedZone("CET", 3600)
	tests := []struct {
		name string
		fail bool
		dir  bool
	}{
		{"file_success", false, false},
		{"file_failure", true, false},
		{"dir_success", false, true},
		{"dir_failure", true, true},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			remote := newMockRemoteFS()
			if tc.fail {
				remote.copyErr = func(string) error { return errors.New("boom") }
				remote.moveDirErr = func(string) error { return errors.New("boom") }
			}
			pub := newMockPublisher()
			cfg := DefaultConfig()
			cfg.Retry.MaxAttempts = 1
			s := New(cfg, remote, newMockStore(), nil, pub, newDiscardLogger(),
				WithSleepFunc(noSleep),
				WithNowFunc(fixedClock(time.Date(2026, 11, 15, 12, 33, 13, 123456789, cet))))
			events := make(chan Event, 1)
			dr := make(chan DirRename, 1)
			if tc.dir {
				dr <- DirRename{From: "a", To: "b"}
			} else {
				events <- Event{Path: "f.txt", Op: OpCreate}
			}
			runSyncer(t, s, events, dr, func() {})

			msgs := pub.getEvents()
			if len(msgs) != 1 {
				t.Fatalf("events = %d, want 1", len(msgs))
			}
			ts, _ := msgs[0].Payload.(map[string]any)["ts"].(string)
			if !re.MatchString(ts) || ts != "2026-11-15T11:33:13Z" {
				t.Errorf("ts = %q, want 2026-11-15T11:33:13Z", ts)
			}
		})
	}
}

func newRecordedPuller(t *testing.T, remote *mockRemoteFS, rec Recorder) *Puller {
	t.Helper()
	return NewPuller(
		PullerConfig{Interval: time.Second, LocalRoot: t.TempDir(), Recorder: rec},
		remote, newMockStore(), newMockAuditLogger(), newMockPublisher(),
		slog.New(slog.NewJSONHandler(discardWriter{}, nil)),
		fixedClock(time.Date(2026, 11, 15, 12, 0, 0, 0, time.UTC)),
	)
}

// TestPullerRecorder_DownloadRecordsBytes is the regression test for bug
// (a): a download must record Dir "down" with Bytes == size.
func TestPullerRecorder_DownloadRecordsBytes(t *testing.T) {
	now := time.Date(2026, 11, 15, 12, 0, 0, 0, time.UTC)
	remote := newMockRemoteFS()
	remote.startToken = "t1"
	remote.changes["t1"] = Changes{
		Items: []Change{{Path: "big.bin", Object: &RemoteObject{
			Path: "big.bin", Size: 20 << 20, MD5: "m", ModTime: now, RemoteID: "id",
		}}},
		NextPageToken: "t2",
	}
	rec := &fakeRecorder{}
	p := newRecordedPuller(t, remote, rec)
	if err := p.PollOnce(context.Background()); err != nil {
		t.Fatalf("PollOnce: %v", err)
	}

	dl := rec.byOp(OutPull)
	if len(dl) != 1 || dl[0].Dir != DirDown || dl[0].Bytes != 20<<20 || dl[0].Err != nil {
		t.Fatalf("download outcomes = %+v", dl)
	}
	if polls := rec.byOp(OutPoll); len(polls) != 1 || polls[0].Err != nil {
		t.Errorf("poll outcomes = %+v", polls)
	}
}

func TestPullerRecorder_Cases(t *testing.T) {
	now := time.Date(2026, 11, 15, 12, 0, 0, 0, time.UTC)
	tests := []struct {
		name       string
		downloadEr error
		deleted    bool
		wantOp     string
		wantN      int
		wantErr    bool
	}{
		{"download_failure", errors.New("net down"), false, OutPull, 1, true},
		{"dangling_shortcut_not_an_error", errDanglingTest, false, OutPull, 0, false},
		{"remote_delete", nil, true, OutPullDelete, 1, false},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			remote := newMockRemoteFS()
			remote.startToken = "t1"
			remote.downloadErr["x.txt"] = tc.downloadEr
			if tc.downloadEr == nil {
				delete(remote.downloadErr, "x.txt")
			}
			remote.changes["t1"] = Changes{
				Items: []Change{{Path: "x.txt", Deleted: tc.deleted, Object: &RemoteObject{
					Path: "x.txt", Size: 5, MD5: "m", ModTime: now, RemoteID: "id",
				}}},
				NextPageToken: "t2",
			}
			rec := &fakeRecorder{}
			p := newRecordedPuller(t, remote, rec)
			_ = p.PollOnce(context.Background())

			got := rec.byOp(tc.wantOp)
			if len(got) != tc.wantN {
				t.Fatalf("%s outcomes = %+v, want %d", tc.wantOp, got, tc.wantN)
			}
			if tc.wantN == 1 && (got[0].Err != nil) != tc.wantErr {
				t.Errorf("outcome = %+v, wantErr %v", got[0], tc.wantErr)
			}
		})
	}
}

func TestPullerRecorder_PollFailure(t *testing.T) {
	remote := newMockRemoteFS()
	remote.startToken = "t1"
	remote.genericErrTokens = map[string]error{"t1": errors.New("503")}
	rec := &fakeRecorder{}
	p := newRecordedPuller(t, remote, rec)
	if err := p.PollOnce(context.Background()); err == nil {
		t.Fatal("expected poll error")
	}
	polls := rec.byOp(OutPoll)
	if len(polls) != 1 || polls[0].Err == nil {
		t.Fatalf("poll outcomes = %+v, want one failure", polls)
	}
}

func TestBisyncRecorder_PassAndTree(t *testing.T) {
	root := t.TempDir()
	now := time.Date(2026, 4, 28, 12, 0, 0, 0, time.UTC)
	createLocalFile(t, root, "docs/readme.txt", now)

	bisync, _, remote, _, _, _ := newTestBisync(t, root, false)
	remote.Seed("remote-only.txt", now, "m")
	rec := &fakeRecorder{}
	bisync.rec = rec
	bisync.execute(context.Background())

	if got := rec.byOp(OutBisyncPush); len(got) != 1 || got[0].Dir != DirUp || got[0].Err != nil {
		t.Errorf("bisync push outcomes = %+v", got)
	}
	if got := rec.byOp(OutBisyncPull); len(got) != 1 || got[0].Dir != DirDown {
		t.Errorf("bisync pull outcomes = %+v", got)
	}
	pass := rec.byOp(OutBisync)
	if len(pass) != 1 || pass[0].Err != nil || pass[0].Files != 2 {
		t.Errorf("pass outcomes = %+v, want one ok pass with 2 files", pass)
	}
	if rec.files != 1 {
		t.Errorf("local tree files = %d, want 1", rec.files)
	}
}

func TestBisyncRecorder_DryRunRecordsNoTransfers(t *testing.T) {
	root := t.TempDir()
	createLocalFile(t, root, "a.txt", time.Date(2026, 4, 28, 12, 0, 0, 0, time.UTC))
	bisync, _, _, _, _, _ := newTestBisync(t, root, true)
	rec := &fakeRecorder{}
	bisync.rec = rec
	bisync.execute(context.Background())
	if n := len(rec.byOp(OutBisyncPush)); n != 0 {
		t.Errorf("dry run recorded %d push outcomes", n)
	}
}

func TestEmitOutcome_NilAndNonCountable(t *testing.T) {
	emitOutcome(nil, Outcome{Op: OutPush}) // must not panic
	rec := &fakeRecorder{}
	emitOutcome(rec, Outcome{Op: OutPull, Err: context.Canceled})
	emitOutcome(rec, Outcome{Op: OutPull, Err: fmt.Errorf("x: %w", ErrDanglingShortcut)})
	if n := len(rec.get()); n != 0 {
		t.Errorf("non-countable errors recorded: %d", n)
	}
}
