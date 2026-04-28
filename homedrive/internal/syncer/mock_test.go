package syncer

import (
	"context"
	"log/slog"
	"sync"
	"testing"
	"time"
)

// --- Test mocks ---

// mockRemoteFS records calls and optionally returns errors.
type mockRemoteFS struct {
	mu        sync.Mutex
	copyCalls []string
	delCalls  []string
	moveCalls [][2]string
	statCalls []string
	copyErr   func(path string) error
	delErr    func(path string) error
	moveErr   func(src, dst string) error
}

func newMockRemoteFS() *mockRemoteFS {
	return &mockRemoteFS{}
}

func (m *mockRemoteFS) CopyFile(_ context.Context, src, _ string) (RemoteObject, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.copyCalls = append(m.copyCalls, src)
	if m.copyErr != nil {
		if err := m.copyErr(src); err != nil {
			return RemoteObject{}, err
		}
	}
	return RemoteObject{Path: src, ModTime: time.Now()}, nil
}

func (m *mockRemoteFS) DeleteFile(_ context.Context, path string) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.delCalls = append(m.delCalls, path)
	if m.delErr != nil {
		return m.delErr(path)
	}
	return nil
}

func (m *mockRemoteFS) MoveFile(_ context.Context, src, dst string) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.moveCalls = append(m.moveCalls, [2]string{src, dst})
	if m.moveErr != nil {
		return m.moveErr(src, dst)
	}
	return nil
}

func (m *mockRemoteFS) Stat(_ context.Context, path string) (RemoteObject, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.statCalls = append(m.statCalls, path)
	return RemoteObject{Path: path}, nil
}

func (m *mockRemoteFS) getCopyCalls() []string {
	m.mu.Lock()
	defer m.mu.Unlock()
	out := make([]string, len(m.copyCalls))
	copy(out, m.copyCalls)
	return out
}

func (m *mockRemoteFS) getDeleteCalls() []string {
	m.mu.Lock()
	defer m.mu.Unlock()
	out := make([]string, len(m.delCalls))
	copy(out, m.delCalls)
	return out
}

func (m *mockRemoteFS) getMoveCalls() [][2]string {
	m.mu.Lock()
	defer m.mu.Unlock()
	out := make([][2]string, len(m.moveCalls))
	copy(out, m.moveCalls)
	return out
}

// mockStore records put/delete/rewrite calls.
type mockStore struct {
	mu            sync.Mutex
	puts          []SyncRecord
	deletes       []string
	rewriteCalls  [][2]string
	rewriteReturn int
}

func newMockStore() *mockStore {
	return &mockStore{rewriteReturn: 42}
}

func (m *mockStore) Get(_ string) (*SyncRecord, error) {
	return nil, nil
}

func (m *mockStore) Put(rec SyncRecord) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.puts = append(m.puts, rec)
	return nil
}

func (m *mockStore) Delete(path string) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.deletes = append(m.deletes, path)
	return nil
}

func (m *mockStore) RewritePrefix(old, new string) (int, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.rewriteCalls = append(m.rewriteCalls, [2]string{old, new})
	return m.rewriteReturn, nil
}

func (m *mockStore) getPuts() []SyncRecord {
	m.mu.Lock()
	defer m.mu.Unlock()
	out := make([]SyncRecord, len(m.puts))
	copy(out, m.puts)
	return out
}

func (m *mockStore) getRewriteCalls() [][2]string {
	m.mu.Lock()
	defer m.mu.Unlock()
	out := make([][2]string, len(m.rewriteCalls))
	copy(out, m.rewriteCalls)
	return out
}

// mockAuditLog records audit entries.
type mockAuditLog struct {
	mu      sync.Mutex
	entries []AuditEntry
}

func newMockAuditLog() *mockAuditLog {
	return &mockAuditLog{}
}

func (m *mockAuditLog) Append(entry AuditEntry) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.entries = append(m.entries, entry)
	return nil
}

func (m *mockAuditLog) getEntries() []AuditEntry {
	m.mu.Lock()
	defer m.mu.Unlock()
	out := make([]AuditEntry, len(m.entries))
	copy(out, m.entries)
	return out
}

// mockPublisher records published events.
type mockPublisher struct {
	mu     sync.Mutex
	events []publishedEvent
}

type publishedEvent struct {
	Topic   string
	Payload any
}

func newMockPublisher() *mockPublisher {
	return &mockPublisher{}
}

func (m *mockPublisher) PublishJSON(topic string, payload any) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.events = append(m.events, publishedEvent{Topic: topic, Payload: payload})
	return nil
}

func (m *mockPublisher) Topic(parts ...string) string {
	result := "homedrive/test"
	for _, p := range parts {
		result += "/" + p
	}
	return result
}

func (m *mockPublisher) getEvents() []publishedEvent {
	m.mu.Lock()
	defer m.mu.Unlock()
	out := make([]publishedEvent, len(m.events))
	copy(out, m.events)
	return out
}

// --- Test helpers ---

// runSyncer starts a syncer, calls setup, then closes channels and
// waits for graceful shutdown.
func runSyncer(
	t *testing.T,
	s *Syncer,
	events chan Event,
	dirRenames chan DirRename,
	setup func(),
) {
	t.Helper()
	// We do NOT cancel the context; closing the input channels is
	// sufficient to make Run return once all items are drained.
	ctx := context.Background()

	done := make(chan struct{})
	go func() {
		s.Run(ctx, events, dirRenames)
		close(done)
	}()

	setup()

	// Closing input channels causes the fan-in goroutines to exit.
	// Run then closes the work channel; workers drain remaining items
	// and return.
	close(events)
	close(dirRenames)

	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("syncer did not shut down in time")
	}
}

// noSleep is a sleep function that returns immediately, for tests.
func noSleep(_ context.Context, _ time.Duration) {}

// newDiscardLogger returns an slog.Logger that discards all output.
func newDiscardLogger() *slog.Logger {
	return slog.New(slog.NewJSONHandler(
		discardWriter{}, &slog.HandlerOptions{Level: slog.LevelError + 1}))
}

type discardWriter struct{}

func (discardWriter) Write(p []byte) (int, error) { return len(p), nil }
