package syncer

import (
	"bytes"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

// ---------------------------------------------------------------------------
// mockClock is a controllable clock for bisync tests.
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
// mockJournal is a thread-safe in-memory journal for bisync tests.
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

func (j *mockJournal) Delete(path string) error {
	j.mu.Lock()
	defer j.mu.Unlock()
	delete(j.entries, path)
	return nil
}

func (j *mockJournal) ListByPrefix(prefix string) ([]JournalEntry, error) {
	j.mu.Lock()
	defer j.mu.Unlock()
	var out []JournalEntry
	for p, e := range j.entries {
		if strings.HasPrefix(p, prefix) {
			out = append(out, e)
		}
	}
	return out, nil
}

func (j *mockJournal) ForEach(fn func(JournalEntry) error) error {
	j.mu.Lock()
	entries := make([]JournalEntry, 0, len(j.entries))
	for _, e := range j.entries {
		entries = append(entries, e)
	}
	j.mu.Unlock()
	for _, e := range entries {
		if err := fn(e); err != nil {
			return err
		}
	}
	return nil
}

// ---------------------------------------------------------------------------
// mockMQTT records bisync MQTT events for assertion.
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
