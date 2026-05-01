package syncer

import (
	"context"
	"fmt"
	"sync"
	"time"
)

// mockRemoteFS is a thread-safe in-memory RemoteFS for testing.
type mockRemoteFS struct {
	mu    sync.Mutex
	files map[string]RemoteObject

	// changes is keyed by page token. Each call to ListChanges returns
	// the Changes at that token and moves to the next.
	changes     map[string]Changes
	startToken  string
	goneTokens  map[string]bool // tokens that trigger ErrGone
	downloadErr map[string]error

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
		files:       make(map[string]RemoteObject),
		changes:     make(map[string]Changes),
		goneTokens:  make(map[string]bool),
		downloadErr: make(map[string]error),
	}
}

func (m *mockRemoteFS) CopyFile(_ context.Context, src, dstDir string) (RemoteObject, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.copiedFiles = append(m.copiedFiles, copyCall{Src: src, DstDir: dstDir})
	obj, ok := m.files[dstDir]
	if !ok {
		obj = RemoteObject{Path: dstDir, ID: "id-" + dstDir}
	}
	return obj, nil
}

func (m *mockRemoteFS) DeleteFile(_ context.Context, path string) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.deletedFiles = append(m.deletedFiles, path)
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
	// Write actual file content so os.Stat works in tests.
	return writeTestFile(localPath, "content-of-"+remotePath)
}

// mockStore is a thread-safe in-memory Store for testing.
type mockStore struct {
	mu        sync.Mutex
	entries   map[string]JournalEntry
	pageToken string
}

func newMockStore() *mockStore {
	return &mockStore{
		entries: make(map[string]JournalEntry),
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

func (s *mockStore) NextOldN(_ context.Context, path string) (int, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	n := 1
	for {
		candidate := fmt.Sprintf("%s.old.%d", path, n)
		if _, ok := s.entries[candidate]; !ok {
			return n, nil
		}
		n++
	}
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
	return &mockPublisher{basePath: "homedrive/testhost/testuser"}
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
