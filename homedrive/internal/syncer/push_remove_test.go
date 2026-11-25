package syncer

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sync/atomic"
	"testing"
	"time"

	"github.com/asnowfix/home-drive/homedrive/internal/rcloneclient"
)

func TestSyncer_ExecRemove_Cases(t *testing.T) {
	tests := []struct {
		name      string
		delErr    error
		wantCalls int
		wantTopic string // "" = no event at all
	}{
		{
			name:      "already absent: one attempt, success",
			delErr:    fmt.Errorf("delete: %w", ErrRemoteNotFound),
			wantCalls: 1,
			wantTopic: "homedrive/test/events/push.success",
		},
		{
			// Not journaled: kept on Drive, no success and no failure event.
			name:      "unknown directory: one attempt, kept, no event",
			delErr:    fmt.Errorf("delete: %w", ErrRemoteIsDir),
			wantCalls: 1,
		},
		{
			name:      "other error: still retried and fails",
			delErr:    errors.New("boom"),
			wantCalls: DefaultConfig().Retry.MaxAttempts,
			wantTopic: "homedrive/test/events/push.failure",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			remote := newMockRemoteFS()
			remote.delErr = func(string) error { return tt.delErr }
			pub := newMockPublisher()
			cfg := DefaultConfig()
			cfg.LocalRoot = t.TempDir()
			s := New(cfg, remote, newMockStore(), newMockAuditLogger(),
				pub, newDiscardLogger(), WithSleepFunc(noSleep))

			events := make(chan Event, 1)
			events <- Event{Path: "docs/gone.txt", Op: OpRemove, At: time.Now()}
			runSyncer(t, s, events, make(chan DirRename), func() {})

			if got := len(remote.getDeleteCalls()); got != tt.wantCalls {
				t.Errorf("DeleteFile calls = %d, want %d", got, tt.wantCalls)
			}
			msgs := pub.getEvents()
			if tt.wantTopic == "" {
				if len(msgs) != 0 {
					t.Errorf("published = %+v, want none", msgs)
				}
				return
			}
			if len(msgs) != 1 || msgs[0].Topic != tt.wantTopic {
				t.Errorf("published = %+v, want one %s", msgs, tt.wantTopic)
			}
		})
	}
}

// dirRemoveEnv is one execRemove directory scenario (issue #174).
type dirRemoveEnv struct {
	root   string
	remote *mockRemoteFS
	store  *mockStore
	audit  *mockAuditLogger
	pub    *mockPublisher
}

func newDirRemoveEnv(t *testing.T) *dirRemoveEnv {
	t.Helper()
	return &dirRemoveEnv{
		root: t.TempDir(), remote: newMockRemoteFS(), store: newMockStore(),
		audit: newMockAuditLogger(), pub: newMockPublisher(),
	}
}

// run pushes one remove event for path through a Syncer over fs.
func (e *dirRemoveEnv) run(t *testing.T, fs RemoteFS, path string, dryRun bool, opts ...Option) {
	t.Helper()
	cfg := DefaultConfig()
	cfg.LocalRoot = e.root
	cfg.DryRun = dryRun
	opts = append(opts, WithSleepFunc(noSleep))
	s := New(cfg, fs, e.store, e.audit, e.pub, newDiscardLogger(), opts...)
	events := make(chan Event, 1)
	events <- Event{Path: path, Op: OpRemove, At: time.Now()}
	runSyncer(t, s, events, make(chan DirRename), func() {})
}

// lastAudit returns the last audit entry, or a zero entry.
func (e *dirRemoveEnv) lastAudit() AuditEntry {
	entries := e.audit.getEntries()
	if len(entries) == 0 {
		return AuditEntry{}
	}
	return entries[len(entries)-1]
}

func TestExecRemove_DirCases(t *testing.T) {
	maxAttempts := DefaultConfig().Retry.MaxAttempts
	synced := func(e *dirRemoveEnv) {
		e.remote.seedDir("d")
		e.store.entries["d/local.txt"] = JournalEntry{Path: "d/local.txt", LastOrigin: "local"}
	}
	tests := []struct {
		name           string
		path           string
		setup          func(e *dirRemoveEnv)
		wantDirGone    bool
		wantRmCalls    int
		wantDelCalls   int
		wantAuditOp    string
		wantResolution string
		wantTopic      string // "" = no event
	}{
		{
			name: "empty synced dir removed", path: "d", setup: synced,
			wantDirGone: true, wantRmCalls: 1, wantDelCalls: 1,
			wantAuditOp: "remove_dir", wantTopic: "homedrive/test/events/push.success",
		},
		{
			name: "recorded dir id counts as synced", path: "d",
			setup: func(e *dirRemoveEnv) {
				e.remote.seedDir("d")
				e.store.entries["d"] = JournalEntry{Path: "d", RemoteID: "folder-id"}
			},
			wantDirGone: true, wantRmCalls: 1, wantDelCalls: 1,
			wantAuditOp: "remove_dir", wantTopic: "homedrive/test/events/push.success",
		},
		{
			name: "dir with an unknown child kept", path: "d",
			setup: func(e *dirRemoveEnv) {
				synced(e)
				e.remote.Seed("d/not-ours.txt", time.Now(), "m")
			},
			wantRmCalls: 0, wantDelCalls: maxAttempts,
			wantAuditOp: "remove_dir_kept", wantResolution: keptNotEmpty,
		},
		{
			// A non-trashed child that List does not return (e.g. an
			// excluded path or a native Google Doc) is still caught by
			// RemoveEmptyDir's own check.
			name: "child only RemoveEmptyDir sees kept", path: "d",
			setup: func(e *dirRemoveEnv) {
				synced(e)
				e.remote.rmDirErr = func(string) error {
					return fmt.Errorf("unlisted child: %w", rcloneclient.ErrDirNotEmpty)
				}
			},
			wantRmCalls: maxAttempts, wantDelCalls: maxAttempts,
			wantAuditOp: "remove_dir_kept", wantResolution: keptNotEmpty,
		},
		{
			name: "unjournaled dir kept", path: "d",
			setup:       func(e *dirRemoveEnv) { e.remote.seedDir("d") },
			wantRmCalls: 0, wantDelCalls: 1,
			wantAuditOp: "remove_dir_kept", wantResolution: keptUnknown,
		},
		{
			name: "dir journal entry without an id kept", path: "d",
			setup: func(e *dirRemoveEnv) {
				e.remote.seedDir("d")
				e.store.entries["d"] = JournalEntry{Path: "d", LastOrigin: "local"}
			},
			wantRmCalls: 0, wantDelCalls: 1,
			wantAuditOp: "remove_dir_kept", wantResolution: keptUnknown,
		},
		{
			name: "recreated locally kept", path: "d",
			setup: func(e *dirRemoveEnv) {
				synced(e)
				if err := os.Mkdir(filepath.Join(e.root, "d"), 0o755); err != nil {
					t.Fatal(err)
				}
			},
			wantRmCalls: 0, wantDelCalls: 1,
			wantAuditOp: "remove_dir_kept", wantResolution: keptLocalExists,
		},
		{
			name: "root path kept", path: "",
			setup: func(e *dirRemoveEnv) {
				e.remote.delErr = func(string) error { return ErrRemoteIsDir }
				e.store.entries["x.txt"] = JournalEntry{Path: "x.txt"}
			},
			wantRmCalls: 0, wantDelCalls: 1,
			wantAuditOp: "remove_dir_kept", wantResolution: keptRootOrBad,
		},
		{
			name: "already absent on Drive is success", path: "d",
			setup: func(e *dirRemoveEnv) {
				e.store.entries["d/local.txt"] = JournalEntry{Path: "d/local.txt"}
			},
			wantDirGone: true, wantRmCalls: 0, wantDelCalls: 1,
			wantAuditOp: "remove", wantTopic: "homedrive/test/events/push.success",
		},
		{
			name: "folder vanished before the trash is success", path: "d",
			setup: func(e *dirRemoveEnv) {
				synced(e)
				e.remote.rmDirErr = func(string) error { return ErrRemoteNotFound }
			},
			wantRmCalls: 1, wantDelCalls: 1,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			e := newDirRemoveEnv(t)
			tt.setup(e)
			e.run(t, e.remote, tt.path, false)

			if got := len(e.remote.getRemovedDirs()); got != tt.wantRmCalls {
				t.Errorf("RemoveEmptyDir calls = %d, want %d", got, tt.wantRmCalls)
			}
			if got := len(e.remote.getDeleteCalls()); got != tt.wantDelCalls {
				t.Errorf("DeleteFile calls = %d, want %d", got, tt.wantDelCalls)
			}
			if tt.path == "d" && e.remote.hasDir("d") == tt.wantDirGone {
				t.Errorf("remote dir present = %v, want gone = %v", e.remote.hasDir("d"), tt.wantDirGone)
			}
			if a := e.lastAudit(); a.Op != tt.wantAuditOp || a.Resolution != tt.wantResolution {
				t.Errorf("audit = %+v, want op %q resolution %q", a, tt.wantAuditOp, tt.wantResolution)
			}
			assertOneTopic(t, e.pub, tt.wantTopic)
			for _, put := range e.store.getPuts() {
				// A folder outcome must not journal the folder path (the
				// seeded entries carry no LastSyncedAt).
				if put.Path == tt.path && !put.LastSyncedAt.IsZero() && tt.wantAuditOp != "remove" {
					t.Errorf("journal entry written for the folder path: %+v", put)
				}
			}
		})
	}
}

// assertOneTopic checks that exactly one event on want (or none) was sent.
func assertOneTopic(t *testing.T, pub *mockPublisher, want string) {
	t.Helper()
	msgs := pub.getEvents()
	if want == "" {
		if len(msgs) != 0 {
			t.Errorf("published = %+v, want none", msgs)
		}
		return
	}
	if len(msgs) != 1 || msgs[0].Topic != want {
		t.Errorf("published = %+v, want one %s", msgs, want)
	}
}

func TestExecRemove_TransientListError(t *testing.T) {
	maxAttempts := DefaultConfig().Retry.MaxAttempts
	tests := []struct {
		name        string
		failures    int32
		wantDirGone bool
		wantTopic   string
	}{
		{name: "recovers after two failures", failures: 2, wantDirGone: true,
			wantTopic: "homedrive/test/events/push.success"},
		{name: "never recovers: never deletes", failures: 1000,
			wantTopic: "homedrive/test/events/push.failure"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			e := newDirRemoveEnv(t)
			e.remote.seedDir("d")
			e.store.entries["d/local.txt"] = JournalEntry{Path: "d/local.txt"}
			var lists atomic.Int32
			flaky := rcloneclient.NewFlakyFS(e.remote, rcloneclient.FlakyRule{
				Method: "List",
				Match:  func(_, _ string) bool { return lists.Add(1) <= tt.failures },
				Action: rcloneclient.FlakyAction{Err: errors.New("transient list error")},
			})
			e.run(t, flaky, "d", false)

			if gone := !e.remote.hasDir("d"); gone != tt.wantDirGone {
				t.Errorf("dir gone = %v, want %v", gone, tt.wantDirGone)
			}
			rm := len(e.remote.getRemovedDirs())
			if !tt.wantDirGone && rm != 0 {
				t.Errorf("RemoveEmptyDir called %d times after failed listings", rm)
			}
			if !tt.wantDirGone && int(lists.Load()) != maxAttempts {
				t.Errorf("List calls = %d, want %d", lists.Load(), maxAttempts)
			}
			assertOneTopic(t, e.pub, tt.wantTopic)
		})
	}
}

func TestExecRemove_DryRunLeavesRemote(t *testing.T) {
	tests := []struct {
		name   string
		dryRun bool
		wrap   bool
	}{
		{name: "syncer dry-run", dryRun: true},
		{name: "DryRunFS", wrap: true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			e := newDirRemoveEnv(t)
			e.remote.seedDir("d")
			e.store.entries["d/local.txt"] = JournalEntry{Path: "d/local.txt"}
			var fs RemoteFS = e.remote
			if tt.wrap {
				fs = rcloneclient.NewDryRunFS(e.remote, newDiscardLogger())
			}
			e.run(t, fs, "d", tt.dryRun)
			if !e.remote.hasDir("d") {
				t.Error("dry-run removed the remote folder")
			}
			if n := len(e.remote.getRemovedDirs()); n != 0 {
				t.Errorf("RemoveEmptyDir reached the remote %d times", n)
			}
		})
	}
}

func TestExecRemove_OwnRemovalSuppressed(t *testing.T) {
	e := newDirRemoveEnv(t)
	e.remote.seedDir("d")
	e.store.entries["d/local.txt"] = JournalEntry{Path: "d/local.txt"}
	dir := filepath.Join(e.root, "d")
	if err := os.Mkdir(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	g := NewDeleteGuard(e.root, osRemover{}, time.Minute, nil)
	if err := g.Remove(dir); err != nil {
		t.Fatalf("guarded remove: %v", err)
	}
	e.run(t, e.remote, "d", false, WithDeleteGuard(g))
	if n := len(e.remote.getDeleteCalls()) + len(e.remote.getRemovedDirs()); n != 0 {
		t.Errorf("own removal reached Drive (%d calls)", n)
	}
	if !e.remote.hasDir("d") {
		t.Error("remote folder removed for homedrive's own local removal")
	}
}

func TestExecRemove_RenamePairingDoesNotDelete(t *testing.T) {
	e := newDirRemoveEnv(t)
	e.remote.seedDir("a")
	e.store.entries["a/f.txt"] = JournalEntry{Path: "a/f.txt"}
	cfg := DefaultConfig()
	cfg.LocalRoot = e.root
	s := New(cfg, e.remote, e.store, e.audit, e.pub, newDiscardLogger(), WithSleepFunc(noSleep))
	events := make(chan Event, 1)
	renames := make(chan DirRename, 1)
	events <- Event{Path: "a", Op: OpRename, At: time.Now()} // unpaired half
	renames <- DirRename{From: "a", To: "b", At: time.Now()}
	runSyncer(t, s, events, renames, func() {})

	if n := len(e.remote.getRemovedDirs()); n != 0 {
		t.Errorf("RemoveEmptyDir called %d times for a rename", n)
	}
	if n := len(e.remote.getDeleteCalls()); n != 0 {
		t.Errorf("DeleteFile called %d times for a rename", n)
	}
	if mv := e.remote.movedDirs; len(mv) != 1 || mv[0] != (moveCall{Src: "a", Dst: "b"}) {
		t.Errorf("MoveDir calls = %+v, want one a -> b", mv)
	}
}

func TestExecRemove_NoPullEchoLoop(t *testing.T) {
	e := newDirRemoveEnv(t)
	e.remote.seedDir("d")
	e.store.entries["d/local.txt"] = JournalEntry{Path: "d/local.txt"}
	e.run(t, e.remote, "d", false)
	if e.remote.hasDir("d") {
		t.Fatal("precondition: folder not trashed by the push")
	}

	// Drive then reports the folder as removed; the pull must not
	// re-create it locally nor download anything.
	e.remote.startToken = "t1"
	e.remote.changes["t1"] = Changes{
		Items:         []Change{{Path: "d", Deleted: true, ID: "folder-id"}},
		NextPageToken: "t2",
	}
	p := NewPuller(PullerConfig{Remover: osRemover{}, Interval: 30 * time.Second,
		LocalRoot: e.root, ConflictPolicy: PolicyNewerWins},
		e.remote, e.store, e.audit, e.pub, newDiscardLogger(), time.Now)
	if err := p.PollOnce(context.Background()); err != nil {
		t.Fatalf("PollOnce: %v", err)
	}
	if _, err := os.Lstat(filepath.Join(e.root, "d")); !os.IsNotExist(err) {
		t.Errorf("pull re-created the folder locally (lstat err = %v)", err)
	}
	if n := len(e.remote.downloadedFiles); n != 0 {
		t.Errorf("pull downloaded %d files", n)
	}
	if n := len(e.remote.getRemovedDirs()); n != 1 {
		t.Errorf("RemoveEmptyDir calls = %d, want 1", n)
	}
}
