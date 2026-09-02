package syncer

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/asnowfix/home-drive/homedrive/internal/oldsuffix"
	"github.com/asnowfix/home-drive/homedrive/internal/rcloneclient"
)

// mockRemoteFS is a thread-safe in-memory RemoteFS for testing.
type mockRemoteFS struct {
	mu    sync.Mutex
	files map[string]RemoteObject

	// changes is keyed by page token. Each call to ListChanges returns
	// the Changes at that token and moves to the next.
	changes     map[string]Changes
	startToken  string
	goneTokens  map[string]bool // tokens that trigger ErrGone (410-style)
	// rejectedTokens simulates the non-410 "Drive never recognized this
	// token" case (issue #64), e.g. HTTP 400 from changes.list. Also
	// triggers the reset path, but errors.Is(err, ErrTokenRejected) is
	// true in addition to errors.Is(err, ErrGone).
	rejectedTokens map[string]bool
	// genericErrTokens simulates a ListChanges failure that is NOT
	// token-related at all (e.g. a 403 rateLimitExceeded, or a transport
	// failure) -- must NOT trigger the reset path (issue #64 PR review
	// item 4: negative coverage for "leaves the token untouched").
	genericErrTokens map[string]error
	downloadErr      map[string]error
	quota            Quota

	// error hooks for push tests
	copyErr func(path string) error
	delErr  func(path string) error

	// call recorders
	downloadedFiles []downloadCall
	copiedFiles     []copyCall
	movedFiles      []moveCall
	deletedFiles    []string
}

type downloadCall struct {
	RemotePath string
	LocalPath  string
}

type copyCall struct {
	Src    string
	DstDir string
}

type moveCall struct {
	Src string
	Dst string
}

func newMockRemoteFS() *mockRemoteFS {
	return &mockRemoteFS{
		files:            make(map[string]RemoteObject),
		changes:          make(map[string]Changes),
		goneTokens:       make(map[string]bool),
		rejectedTokens:   make(map[string]bool),
		genericErrTokens: make(map[string]error),
		downloadErr:      make(map[string]error),
	}
}

func (m *mockRemoteFS) CopyFile(_ context.Context, src, dstDir string) (RemoteObject, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.copiedFiles = append(m.copiedFiles, copyCall{Src: src, DstDir: dstDir})
	if m.copyErr != nil {
		if err := m.copyErr(src); err != nil {
			return RemoteObject{}, err
		}
	}
	// Compute destination path. Two calling conventions coexist:
	//   push syncer: CopyFile(relPath, relPath)     → dstDir IS the full path
	//   bisync:      CopyFile(absLocalPath, dir)     → join dir + basename(src)
	name := filepath.Base(src)
	remotePath := dstDir
	if remotePath == "" || remotePath == "." {
		remotePath = name
	} else if filepath.Base(remotePath) != name {
		remotePath = remotePath + "/" + name
	}
	obj := RemoteObject{Path: remotePath, Size: 100, MD5: "md5-" + remotePath, ModTime: time.Now(), RemoteID: "id-" + remotePath}
	m.files[remotePath] = obj
	return obj, nil
}

func (m *mockRemoteFS) DeleteFile(_ context.Context, path string) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.deletedFiles = append(m.deletedFiles, path)
	if m.delErr != nil {
		if err := m.delErr(path); err != nil {
			return err
		}
	}
	delete(m.files, path)
	return nil
}

func (m *mockRemoteFS) MoveFile(_ context.Context, src, dst string) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.movedFiles = append(m.movedFiles, moveCall{Src: src, Dst: dst})
	if obj, ok := m.files[src]; ok {
		obj.Path = dst
		m.files[dst] = obj
		delete(m.files, src)
	}
	return nil
}

func (m *mockRemoteFS) Stat(_ context.Context, path string) (RemoteObject, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	obj, ok := m.files[path]
	if !ok {
		return RemoteObject{}, fmt.Errorf("not found: %s", path)
	}
	return obj, nil
}

func (m *mockRemoteFS) ListChanges(_ context.Context, pageToken string) (Changes, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.goneTokens[pageToken] {
		return Changes{}, ErrGone
	}
	if m.rejectedTokens[pageToken] {
		// Built only through the shared constructor, same as the
		// production call site (driveapi.go's pollChanges) -- see
		// rcloneclient.NewTokenRejectedErr's doc comment for why hand-
		// composing ErrGone/ErrTokenRejected at each call site is exactly
		// the mistake this guards against (issue #64 PR review item 2).
		return Changes{}, fmt.Errorf("mock changes.list: %w", rcloneclient.NewTokenRejectedErr(errors.New("mock: bad request")))
	}
	if genericErr, ok := m.genericErrTokens[pageToken]; ok {
		return Changes{}, genericErr
	}
	ch, ok := m.changes[pageToken]
	if !ok {
		return Changes{NextPageToken: pageToken}, nil
	}
	return ch, nil
}

func (m *mockRemoteFS) GetStartPageToken(_ context.Context) (string, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.startToken, nil
}

func (m *mockRemoteFS) DownloadFile(_ context.Context, remotePath, localPath string) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.downloadedFiles = append(m.downloadedFiles, downloadCall{
		RemotePath: remotePath,
		LocalPath:  localPath,
	})
	if err, ok := m.downloadErr[remotePath]; ok {
		return err
	}
	return writeTestFile(localPath, "content-of-"+remotePath)
}

// List returns all seeded/copied files (for bisync tests).
func (m *mockRemoteFS) List(_ context.Context, _ string) ([]RemoteObject, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	result := make([]RemoteObject, 0, len(m.files))
	for _, obj := range m.files {
		result = append(result, obj)
	}
	return result, nil
}

// Quota implements RemoteFS (canonical rcloneclient.RemoteFS). It is
// unused by push/pull/bisync but required to satisfy the interface.
func (m *mockRemoteFS) Quota(_ context.Context) (Quota, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.quota, nil
}

// Seed adds a file to the remote store (for bisync tests).
func (m *mockRemoteFS) Seed(path string, modTime time.Time, md5 string) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.files[path] = RemoteObject{
		Path:     path,
		Size:     100,
		MD5:      md5,
		ModTime:  modTime,
		RemoteID: "id-" + path,
	}
}

// HasFile reports whether path exists in the remote store (for bisync tests).
func (m *mockRemoteFS) HasFile(path string) bool {
	m.mu.Lock()
	defer m.mu.Unlock()
	_, ok := m.files[path]
	return ok
}

// CopyCount returns the number of successful CopyFile calls (for bisync tests).
func (m *mockRemoteFS) CopyCount() int {
	m.mu.Lock()
	defer m.mu.Unlock()
	return len(m.copiedFiles)
}

// MoveCount returns the number of MoveFile calls (for bisync tests).
func (m *mockRemoteFS) MoveCount() int {
	m.mu.Lock()
	defer m.mu.Unlock()
	return len(m.movedFiles)
}

// getCopyCalls returns the src paths from all CopyFile calls (for push tests).
func (m *mockRemoteFS) getCopyCalls() []string {
	m.mu.Lock()
	defer m.mu.Unlock()
	out := make([]string, len(m.copiedFiles))
	for i, c := range m.copiedFiles {
		out[i] = c.Src
	}
	return out
}

// getDeleteCalls returns the paths from all DeleteFile calls (for push tests).
func (m *mockRemoteFS) getDeleteCalls() []string {
	m.mu.Lock()
	defer m.mu.Unlock()
	out := make([]string, len(m.deletedFiles))
	copy(out, m.deletedFiles)
	return out
}

// getMoveCalls returns [src, dst] pairs from all MoveFile calls (for push tests).
func (m *mockRemoteFS) getMoveCalls() [][2]string {
	m.mu.Lock()
	defer m.mu.Unlock()
	out := make([][2]string, len(m.movedFiles))
	for i, mv := range m.movedFiles {
		out[i] = [2]string{mv.Src, mv.Dst}
	}
	return out
}

// mockStore is a thread-safe in-memory Store for testing.
type mockStore struct {
	mu           sync.Mutex
	entries      map[string]JournalEntry
	pageToken    string
	rewriteCalls [][2]string
	rewriteCount int

	// matcher controls NextOldN's suffix parsing/formatting. Defaults to
	// the default ".old.%d" format when nil (see newMockStore).
	matcher *oldsuffix.Matcher
}

func newMockStore() *mockStore {
	m, _ := oldsuffix.New("") // never errors for the empty/default format
	return &mockStore{
		entries:      make(map[string]JournalEntry),
		rewriteCount: 42,
		matcher:      m,
	}
}

func (s *mockStore) GetPageToken(_ context.Context) (string, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.pageToken, nil
}

func (s *mockStore) SetPageToken(_ context.Context, token string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.pageToken = token
	return nil
}

func (s *mockStore) Get(_ context.Context, path string) (JournalEntry, bool, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	e, ok := s.entries[path]
	return e, ok, nil
}

func (s *mockStore) Put(_ context.Context, entry JournalEntry) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.entries[entry.Path] = entry
	return nil
}

func (s *mockStore) Delete(_ context.Context, path string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	delete(s.entries, path)
	return nil
}

func (s *mockStore) NextOldN(_ context.Context, path string) (string, int, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	exists := func(p string) bool {
		_, ok := s.entries[p]
		return ok
	}
	base, n := oldsuffix.NextOldN(s.matcher, path, exists)
	return base, n, nil
}

func (s *mockStore) ListOldSiblings(_ context.Context, prefix string) ([]JournalEntry, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	var out []JournalEntry
	for p, e := range s.entries {
		if strings.HasPrefix(p, prefix) {
			out = append(out, e)
		}
	}
	return out, nil
}

func (s *mockStore) ForEach(_ context.Context, fn func(JournalEntry) error) error {
	s.mu.Lock()
	entries := make([]JournalEntry, 0, len(s.entries))
	for _, e := range s.entries {
		entries = append(entries, e)
	}
	s.mu.Unlock()
	for _, e := range entries {
		if err := fn(e); err != nil {
			return err
		}
	}
	return nil
}

func (s *mockStore) RewritePrefix(_ context.Context, oldPrefix, newPrefix string) (int, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.rewriteCalls = append(s.rewriteCalls, [2]string{oldPrefix, newPrefix})
	return s.rewriteCount, nil
}

// getPuts returns all journal entries written via Put (for push tests).
func (s *mockStore) getPuts() []JournalEntry {
	s.mu.Lock()
	defer s.mu.Unlock()
	out := make([]JournalEntry, 0, len(s.entries))
	for _, e := range s.entries {
		out = append(out, e)
	}
	return out
}

// getRewriteCalls returns [oldPrefix, newPrefix] pairs (for push tests).
func (s *mockStore) getRewriteCalls() [][2]string {
	s.mu.Lock()
	defer s.mu.Unlock()
	out := make([][2]string, len(s.rewriteCalls))
	copy(out, s.rewriteCalls)
	return out
}

// mockAuditLogger records audit entries for test assertions.
type mockAuditLogger struct {
	mu      sync.Mutex
	entries []AuditEntry
}

func newMockAuditLogger() *mockAuditLogger {
	return &mockAuditLogger{}
}

func (a *mockAuditLogger) Log(entry AuditEntry) error {
	a.mu.Lock()
	defer a.mu.Unlock()
	a.entries = append(a.entries, entry)
	return nil
}

func (a *mockAuditLogger) getEntries() []AuditEntry {
	a.mu.Lock()
	defer a.mu.Unlock()
	cp := make([]AuditEntry, len(a.entries))
	copy(cp, a.entries)
	return cp
}

// mockPublisher records published MQTT messages for test assertions.
type mockPublisher struct {
	mu       sync.Mutex
	messages []publishedMsg
	basePath string
}

type publishedMsg struct {
	Topic   string
	Payload any
}

func newMockPublisher() *mockPublisher {
	return &mockPublisher{basePath: "homedrive/test"}
}

func (p *mockPublisher) PublishJSON(topic string, payload any) error {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.messages = append(p.messages, publishedMsg{
		Topic:   topic,
		Payload: payload,
	})
	return nil
}

func (p *mockPublisher) Topic(parts ...string) string {
	result := p.basePath
	for _, part := range parts {
		result += "/" + part
	}
	return result
}

func (p *mockPublisher) getMessages() []publishedMsg {
	p.mu.Lock()
	defer p.mu.Unlock()
	cp := make([]publishedMsg, len(p.messages))
	copy(cp, p.messages)
	return cp
}

// getEvents is an alias for getMessages used by push syncer tests.
func (p *mockPublisher) getEvents() []publishedMsg {
	return p.getMessages()
}

func (p *mockPublisher) getMessagesByTopic(topic string) []publishedMsg {
	p.mu.Lock()
	defer p.mu.Unlock()
	var result []publishedMsg
	for _, msg := range p.messages {
		if msg.Topic == topic {
			result = append(result, msg)
		}
	}
	return result
}

// fixedClock returns a clock function that always returns the same time.
func fixedClock(t time.Time) func() time.Time {
	return func() time.Time { return t }
}

// --- Push syncer test helpers ---

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
	ctx := context.Background()

	done := make(chan struct{})
	go func() {
		s.Run(ctx, events, dirRenames)
		close(done)
	}()

	setup()

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
