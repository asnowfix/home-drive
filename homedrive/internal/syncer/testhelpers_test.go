package syncer

import (
	"bytes"
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

// ---------------------------------------------------------------------------
// mockClock is a controllable clock for tests.
// ---------------------------------------------------------------------------

type mockClock struct {
	mu  sync.Mutex
	now time.Time
}

func newMockClock(t time.Time) *mockClock {
	return &mockClock{now: t}
}

func (c *mockClock) Now() time.Time {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.now
}

func (c *mockClock) Advance(d time.Duration) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.now = c.now.Add(d)
}

func (c *mockClock) NewTicker(_ time.Duration) (<-chan time.Time, func()) {
	ch := make(chan time.Time, 1)
	done := make(chan struct{})
	stopped := &atomic.Bool{}

	go func() {
		for {
			select {
			case <-done:
				return
			case <-time.After(10 * time.Millisecond):
				if stopped.Load() {
					return
				}
			}
		}
	}()

	stop := func() {
		stopped.Store(true)
		close(done)
	}
	return ch, stop
}

func (c *mockClock) After(_ time.Duration) <-chan time.Time {
	ch := make(chan time.Time, 1)
	go func() {
		time.Sleep(10 * time.Millisecond)
		c.mu.Lock()
		ch <- c.now
		c.mu.Unlock()
	}()
	return ch
}

// ---------------------------------------------------------------------------
// mockRemoteFS is a thread-safe in-memory remote filesystem.
// ---------------------------------------------------------------------------

type mockRemoteFS struct {
	mu    sync.Mutex
	files map[string]RemoteObject
	// Track operations for assertions.
	copies []string
	moves  []string
}

func newMockRemoteFS() *mockRemoteFS {
	return &mockRemoteFS{
		files: make(map[string]RemoteObject),
	}
}

func (m *mockRemoteFS) Seed(
	path string, modTime time.Time, md5 string,
) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.files[path] = RemoteObject{
		Path:    path,
		Size:    100,
		MD5:     md5,
		ModTime: modTime,
	}
}

func (m *mockRemoteFS) CopyFile(
	_ context.Context, src, dstDir string,
) (RemoteObject, error) {
	m.mu.Lock()
	defer m.mu.Unlock()

	name := filepath.Base(src)
	remotePath := name
	if dstDir != "" {
		remotePath = dstDir + "/" + name
	}

	obj := RemoteObject{
		Path:    remotePath,
		Size:    100,
		MD5:     "md5-" + remotePath,
		ModTime: time.Now(),
	}
	m.files[remotePath] = obj
	m.copies = append(m.copies, remotePath)
	return obj, nil
}

func (m *mockRemoteFS) DeleteFile(
	_ context.Context, path string,
) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	delete(m.files, path)
	return nil
}

func (m *mockRemoteFS) MoveFile(
	_ context.Context, src, dst string,
) error {
	m.mu.Lock()
	defer m.mu.Unlock()

	obj, ok := m.files[src]
	if !ok {
		return fmt.Errorf("remote file not found: %s", src)
	}
	delete(m.files, src)
	obj.Path = dst
	m.files[dst] = obj
	m.moves = append(m.moves, src+"->"+dst)
	return nil
}

func (m *mockRemoteFS) Stat(
	_ context.Context, path string,
) (RemoteObject, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	obj, ok := m.files[path]
	if !ok {
		return RemoteObject{}, fmt.Errorf("not found: %s", path)
	}
	return obj, nil
}

func (m *mockRemoteFS) List(
	_ context.Context, _ string,
) ([]RemoteObject, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	result := make([]RemoteObject, 0, len(m.files))
	for _, obj := range m.files {
		result = append(result, obj)
	}
	return result, nil
}

func (m *mockRemoteFS) CopyCount() int {
	m.mu.Lock()
	defer m.mu.Unlock()
	return len(m.copies)
}

func (m *mockRemoteFS) MoveCount() int {
	m.mu.Lock()
	defer m.mu.Unlock()
	return len(m.moves)
}

func (m *mockRemoteFS) HasFile(path string) bool {
	m.mu.Lock()
	defer m.mu.Unlock()
	_, ok := m.files[path]
	return ok
}

// ---------------------------------------------------------------------------
// mockJournal is a thread-safe in-memory journal.
// ---------------------------------------------------------------------------

type mockJournal struct {
	mu      sync.Mutex
	entries map[string]JournalEntry
}

func newMockJournal() *mockJournal {
	return &mockJournal{
		entries: make(map[string]JournalEntry),
	}
}

func (j *mockJournal) Get(path string) (*JournalEntry, error) {
	j.mu.Lock()
	defer j.mu.Unlock()
	e, ok := j.entries[path]
	if !ok {
		return nil, nil
	}
	return &e, nil
}

func (j *mockJournal) Put(entry JournalEntry) error {
	j.mu.Lock()
	defer j.mu.Unlock()
	j.entries[entry.Path] = entry
	return nil
}

func (j *mockJournal) Exists(path string) bool {
	j.mu.Lock()
	defer j.mu.Unlock()
	_, ok := j.entries[path]
	return ok
}

func (j *mockJournal) Seed(entry JournalEntry) {
	j.mu.Lock()
	defer j.mu.Unlock()
	j.entries[entry.Path] = entry
}

// ---------------------------------------------------------------------------
// mockMQTT records published events for assertion.
// ---------------------------------------------------------------------------

type mockMQTT struct {
	mu     sync.Mutex
	events []BisyncEvent
}

func newMockMQTT() *mockMQTT {
	return &mockMQTT{}
}

func (m *mockMQTT) PublishJSON(_ string, payload any) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	if ev, ok := payload.(BisyncEvent); ok {
		m.events = append(m.events, ev)
	}
	return nil
}

func (m *mockMQTT) Topic(parts ...string) string {
	return "homedrive/test/" + strings.Join(parts, "/")
}

func (m *mockMQTT) Events() []BisyncEvent {
	m.mu.Lock()
	defer m.mu.Unlock()
	cp := make([]BisyncEvent, len(m.events))
	copy(cp, m.events)
	return cp
}

// ---------------------------------------------------------------------------
// threadSafeBuffer is a bytes.Buffer safe for concurrent use.
// ---------------------------------------------------------------------------

type threadSafeBuffer struct {
	mu  sync.Mutex
	buf bytes.Buffer
}

func (b *threadSafeBuffer) Write(p []byte) (n int, err error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.buf.Write(p)
}

func (b *threadSafeBuffer) String() string {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.buf.String()
}

// ---------------------------------------------------------------------------
// Shared test helpers
// ---------------------------------------------------------------------------

func createLocalFile(
	t *testing.T, root, relPath string, modTime time.Time,
) {
	t.Helper()
	fullPath := filepath.Join(root, filepath.FromSlash(relPath))
	if err := os.MkdirAll(filepath.Dir(fullPath), 0o755); err != nil {
		t.Fatalf("mkdir %s: %v", filepath.Dir(fullPath), err)
	}
	if err := os.WriteFile(
		fullPath, []byte("content-"+relPath), 0o644,
	); err != nil {
		t.Fatalf("write %s: %v", fullPath, err)
	}
	if err := os.Chtimes(fullPath, modTime, modTime); err != nil {
		t.Fatalf("chtimes %s: %v", fullPath, err)
	}
}

func newTestBisync(
	t *testing.T,
	localRoot string,
	dryRun bool,
) (
	*Bisync, chan<- struct{},
	*mockRemoteFS, *mockJournal, *mockMQTT, *threadSafeBuffer,
) {
	t.Helper()

	remote := newMockRemoteFS()
	journal := newMockJournal()
	mqtt := newMockMQTT()
	audit := &threadSafeBuffer{}
	clk := newMockClock(
		time.Date(2026, 4, 28, 12, 0, 0, 0, time.UTC),
	)
	mu := &sync.RWMutex{}

	bisync, forceCh := NewBisync(BisyncOpts{
		Config: BisyncConfig{
			Interval:  time.Hour,
			LocalRoot: localRoot,
			DryRun:    dryRun,
		},
		Remote:  remote,
		Journal: journal,
		MQTT:    mqtt,
		Audit:   audit,
		Clock:   clk,
		Mu:      mu,
	})

	return bisync, forceCh, remote, journal, mqtt, audit
}
