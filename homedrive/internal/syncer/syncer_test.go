package syncer

import (
	"context"
	"fmt"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

func TestSyncer_FileCreate(t *testing.T) {
	remote := newMockRemoteFS()
	store := newMockStore()
	audit := newMockAuditLog()
	pub := newMockPublisher()
	logger := newDiscardLogger()

	s := New(DefaultConfig(), remote, store, audit, pub, logger,
		WithSleepFunc(noSleep))

	events := make(chan Event, 1)
	dirRenames := make(chan DirRename)

	events <- Event{Path: "docs/hello.txt", Op: OpCreate, At: time.Now()}

	runSyncer(t, s, events, dirRenames, func() {})

	copies := remote.getCopyCalls()
	if len(copies) != 1 {
		t.Fatalf("expected 1 CopyFile call, got %d", len(copies))
	}
	if copies[0] != "docs/hello.txt" {
		t.Errorf("expected CopyFile for docs/hello.txt, got %s", copies[0])
	}

	puts := store.getPuts()
	if len(puts) != 1 {
		t.Fatalf("expected 1 store Put, got %d", len(puts))
	}
	if puts[0].Path != "docs/hello.txt" {
		t.Errorf("expected store record for docs/hello.txt, got %s", puts[0].Path)
	}
	if puts[0].LastOrigin != "local" {
		t.Errorf("expected origin local, got %s", puts[0].LastOrigin)
	}

	// Verify MQTT push.success event was published.
	pubEvents := pub.getEvents()
	if len(pubEvents) != 1 {
		t.Fatalf("expected 1 publish event, got %d", len(pubEvents))
	}
	if pubEvents[0].Topic != "homedrive/test/events/push.success" {
		t.Errorf("expected push.success topic, got %s", pubEvents[0].Topic)
	}
}

func TestSyncer_FileWrite(t *testing.T) {
	remote := newMockRemoteFS()
	store := newMockStore()
	audit := newMockAuditLog()
	pub := newMockPublisher()
	logger := newDiscardLogger()

	s := New(DefaultConfig(), remote, store, audit, pub, logger,
		WithSleepFunc(noSleep))

	events := make(chan Event, 1)
	dirRenames := make(chan DirRename)

	events <- Event{Path: "docs/modified.txt", Op: OpWrite, At: time.Now()}

	runSyncer(t, s, events, dirRenames, func() {})

	copies := remote.getCopyCalls()
	if len(copies) != 1 {
		t.Fatalf("expected 1 CopyFile call, got %d", len(copies))
	}
	if copies[0] != "docs/modified.txt" {
		t.Errorf("expected CopyFile for docs/modified.txt, got %s", copies[0])
	}
}

func TestSyncer_FileDelete(t *testing.T) {
	remote := newMockRemoteFS()
	store := newMockStore()
	audit := newMockAuditLog()
	pub := newMockPublisher()
	logger := newDiscardLogger()

	s := New(DefaultConfig(), remote, store, audit, pub, logger,
		WithSleepFunc(noSleep))

	events := make(chan Event, 1)
	dirRenames := make(chan DirRename)

	events <- Event{Path: "docs/removed.txt", Op: OpRemove, At: time.Now()}

	runSyncer(t, s, events, dirRenames, func() {})

	deletes := remote.getDeleteCalls()
	if len(deletes) != 1 {
		t.Fatalf("expected 1 DeleteFile call, got %d", len(deletes))
	}
	if deletes[0] != "docs/removed.txt" {
		t.Errorf("expected DeleteFile for docs/removed.txt, got %s", deletes[0])
	}

	pubEvents := pub.getEvents()
	if len(pubEvents) != 1 {
		t.Fatalf("expected 1 publish event, got %d", len(pubEvents))
	}
	if pubEvents[0].Topic != "homedrive/test/events/push.success" {
		t.Errorf("expected push.success topic, got %s", pubEvents[0].Topic)
	}
}

func TestSyncer_DirRename(t *testing.T) {
	remote := newMockRemoteFS()
	store := newMockStore()
	audit := newMockAuditLog()
	pub := newMockPublisher()
	logger := newDiscardLogger()

	s := New(DefaultConfig(), remote, store, audit, pub, logger,
		WithSleepFunc(noSleep))

	events := make(chan Event)
	dirRenames := make(chan DirRename, 1)

	dirRenames <- DirRename{From: "old_dir", To: "new_dir", At: time.Now()}

	runSyncer(t, s, events, dirRenames, func() {})

	moves := remote.getMoveCalls()
	if len(moves) != 1 {
		t.Fatalf("expected exactly 1 MoveFile call, got %d", len(moves))
	}
	if moves[0][0] != "old_dir" || moves[0][1] != "new_dir" {
		t.Errorf("expected MoveFile(old_dir, new_dir), got MoveFile(%s, %s)",
			moves[0][0], moves[0][1])
	}

	// Verify store prefix rewrite was called.
	rewrites := store.getRewriteCalls()
	if len(rewrites) != 1 {
		t.Fatalf("expected 1 RewritePrefix call, got %d", len(rewrites))
	}
	if rewrites[0][0] != "old_dir" || rewrites[0][1] != "new_dir" {
		t.Errorf("expected RewritePrefix(old_dir, new_dir), got (%s, %s)",
			rewrites[0][0], rewrites[0][1])
	}

	// Verify no CopyFile or DeleteFile calls were made.
	if len(remote.getCopyCalls()) != 0 {
		t.Error("expected no CopyFile calls for dir rename")
	}
	if len(remote.getDeleteCalls()) != 0 {
		t.Error("expected no DeleteFile calls for dir rename")
	}
}

func TestSyncer_RetryOnTransientError(t *testing.T) {
	remote := newMockRemoteFS()
	store := newMockStore()
	audit := newMockAuditLog()
	pub := newMockPublisher()
	logger := newDiscardLogger()

	// Fail the first 2 attempts, succeed on the 3rd.
	var callCount atomic.Int32
	remote.copyErr = func(_ string) error {
		n := callCount.Add(1)
		if n <= 2 {
			return fmt.Errorf("transient network error")
		}
		return nil
	}

	s := New(DefaultConfig(), remote, store, audit, pub, logger,
		WithSleepFunc(noSleep))

	events := make(chan Event, 1)
	dirRenames := make(chan DirRename)

	events <- Event{Path: "retry/file.txt", Op: OpCreate, At: time.Now()}

	runSyncer(t, s, events, dirRenames, func() {})

	copies := remote.getCopyCalls()
	if len(copies) != 3 {
		t.Fatalf("expected 3 CopyFile attempts, got %d", len(copies))
	}

	// Should have succeeded, so push.success event emitted.
	pubEvents := pub.getEvents()
	if len(pubEvents) != 1 {
		t.Fatalf("expected 1 publish event, got %d", len(pubEvents))
	}
	if pubEvents[0].Topic != "homedrive/test/events/push.success" {
		t.Errorf("expected push.success, got %s", pubEvents[0].Topic)
	}
}

func TestSyncer_AllRetriesExhausted(t *testing.T) {
	remote := newMockRemoteFS()
	store := newMockStore()
	audit := newMockAuditLog()
	pub := newMockPublisher()
	logger := newDiscardLogger()

	// Always fail.
	remote.copyErr = func(_ string) error {
		return fmt.Errorf("permanent error")
	}

	cfg := DefaultConfig()
	cfg.Retry.MaxAttempts = 3

	s := New(cfg, remote, store, audit, pub, logger,
		WithSleepFunc(noSleep))

	events := make(chan Event, 1)
	dirRenames := make(chan DirRename)

	events <- Event{Path: "fail/file.txt", Op: OpCreate, At: time.Now()}

	runSyncer(t, s, events, dirRenames, func() {})

	copies := remote.getCopyCalls()
	if len(copies) != 3 {
		t.Fatalf("expected 3 CopyFile attempts, got %d", len(copies))
	}

	// No store record should be written on failure.
	puts := store.getPuts()
	if len(puts) != 0 {
		t.Errorf("expected no store puts on failure, got %d", len(puts))
	}

	// push.failure event must be emitted.
	pubEvents := pub.getEvents()
	if len(pubEvents) != 1 {
		t.Fatalf("expected 1 publish event, got %d", len(pubEvents))
	}
	if pubEvents[0].Topic != "homedrive/test/events/push.failure" {
		t.Errorf("expected push.failure, got %s", pubEvents[0].Topic)
	}

	// Audit log should record the failure.
	entries := audit.getEntries()
	if len(entries) != 1 {
		t.Fatalf("expected 1 audit entry, got %d", len(entries))
	}
	if entries[0].Error == "" {
		t.Error("expected audit entry to contain error")
	}
}

func TestSyncer_DryRun(t *testing.T) {
	remote := newMockRemoteFS()
	store := newMockStore()
	audit := newMockAuditLog()
	pub := newMockPublisher()
	logger := newDiscardLogger()

	cfg := DefaultConfig()
	cfg.DryRun = true

	s := New(cfg, remote, store, audit, pub, logger,
		WithSleepFunc(noSleep))

	events := make(chan Event, 3)
	dirRenames := make(chan DirRename, 1)

	events <- Event{Path: "dry/create.txt", Op: OpCreate, At: time.Now()}
	events <- Event{Path: "dry/delete.txt", Op: OpRemove, At: time.Now()}
	events <- Event{Path: "dry/write.txt", Op: OpWrite, At: time.Now()}
	dirRenames <- DirRename{From: "dry/old", To: "dry/new", At: time.Now()}

	runSyncer(t, s, events, dirRenames, func() {})

	// No remote calls should have been made.
	if len(remote.getCopyCalls()) != 0 {
		t.Error("expected no CopyFile calls in dry-run")
	}
	if len(remote.getDeleteCalls()) != 0 {
		t.Error("expected no DeleteFile calls in dry-run")
	}
	if len(remote.getMoveCalls()) != 0 {
		t.Error("expected no MoveFile calls in dry-run")
	}

	// No store writes.
	if len(store.getPuts()) != 0 {
		t.Error("expected no store puts in dry-run")
	}
	if len(store.getRewriteCalls()) != 0 {
		t.Error("expected no store rewrites in dry-run")
	}

	// Audit log should have dry-run entries.
	entries := audit.getEntries()
	if len(entries) != 4 {
		t.Fatalf("expected 4 audit entries in dry-run, got %d", len(entries))
	}
	for _, e := range entries {
		if !e.DryRun {
			t.Errorf("expected all audit entries to be dry_run, got %+v", e)
		}
	}
}

func TestSyncer_ConcurrentEvents(t *testing.T) {
	remote := newMockRemoteFS()
	store := newMockStore()
	audit := newMockAuditLog()
	pub := newMockPublisher()
	logger := newDiscardLogger()

	cfg := DefaultConfig()
	cfg.Workers = 4

	s := New(cfg, remote, store, audit, pub, logger,
		WithSleepFunc(noSleep))

	const numEvents = 50
	events := make(chan Event, numEvents)
	dirRenames := make(chan DirRename)

	for i := 0; i < numEvents; i++ {
		events <- Event{
			Path: fmt.Sprintf("concurrent/file_%03d.txt", i),
			Op:   OpCreate,
			At:   time.Now(),
		}
	}

	runSyncer(t, s, events, dirRenames, func() {})

	copies := remote.getCopyCalls()
	if len(copies) != numEvents {
		t.Fatalf("expected %d CopyFile calls, got %d", numEvents, len(copies))
	}

	puts := store.getPuts()
	if len(puts) != numEvents {
		t.Fatalf("expected %d store puts, got %d", numEvents, len(puts))
	}

	pubEvents := pub.getEvents()
	if len(pubEvents) != numEvents {
		t.Fatalf("expected %d publish events, got %d", numEvents, len(pubEvents))
	}
}

func TestSyncer_BisyncMutexCoordination(t *testing.T) {
	remote := newMockRemoteFS()
	store := newMockStore()
	audit := newMockAuditLog()
	pub := newMockPublisher()
	logger := newDiscardLogger()

	bisyncMu := &sync.RWMutex{}

	s := New(DefaultConfig(), remote, store, audit, pub, logger,
		WithSleepFunc(noSleep),
		WithBisyncMutex(bisyncMu))

	events := make(chan Event, 1)
	dirRenames := make(chan DirRename)

	// Hold the write lock (simulating bisync) then release it.
	bisyncMu.Lock()

	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() {
		s.Run(ctx, events, dirRenames)
		close(done)
	}()

	events <- Event{Path: "blocked/file.txt", Op: OpCreate, At: time.Now()}

	// Give the worker a moment to attempt the lock.
	time.Sleep(50 * time.Millisecond)

	// At this point no CopyFile should have been called (bisync holds lock).
	copies := remote.getCopyCalls()
	if len(copies) != 0 {
		t.Errorf("expected 0 CopyFile calls while bisync lock held, got %d",
			len(copies))
	}

	// Release bisync lock so the push can proceed.
	bisyncMu.Unlock()

	// Close channels and cancel to shut down.
	close(events)
	close(dirRenames)

	// Wait briefly for the event to be processed before cancelling.
	time.Sleep(100 * time.Millisecond)
	cancel()

	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("syncer did not shut down in time")
	}

	copies = remote.getCopyCalls()
	if len(copies) != 1 {
		t.Fatalf("expected 1 CopyFile call after lock released, got %d",
			len(copies))
	}
}

func TestSyncer_NilPublisherAndAudit(t *testing.T) {
	remote := newMockRemoteFS()
	store := newMockStore()
	logger := newDiscardLogger()

	// nil publisher and audit log should not panic.
	s := New(DefaultConfig(), remote, store, nil, nil, logger,
		WithSleepFunc(noSleep))

	events := make(chan Event, 1)
	dirRenames := make(chan DirRename)

	events <- Event{Path: "test/file.txt", Op: OpCreate, At: time.Now()}

	runSyncer(t, s, events, dirRenames, func() {})

	copies := remote.getCopyCalls()
	if len(copies) != 1 {
		t.Fatalf("expected 1 CopyFile call, got %d", len(copies))
	}
}
