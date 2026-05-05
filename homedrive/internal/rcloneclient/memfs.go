package rcloneclient

import (
	"context"
	"fmt"
	"path"
	"sync"
	"time"
)

// Clock abstracts time.Now for testability. Production code passes
// a real clock; tests pass a mock that can be advanced manually.
type Clock interface {
	Now() time.Time
}

// RealClock returns time.Now().
type RealClock struct{}

// Now returns the current time.
func (RealClock) Now() time.Time { return time.Now() }

// MemFSOption configures a MemFS instance.
type MemFSOption func(*MemFS)

// WithClock sets the clock used by MemFS for timestamps.
func WithClock(c Clock) MemFSOption {
	return func(m *MemFS) { m.clock = c }
}

// MemFS is an in-memory, thread-safe implementation of RemoteFS for tests.
// It simulates a remote filesystem with deterministic behavior.
type MemFS struct {
	mu      sync.Mutex
	files   map[string]RemoteObject
	clock   Clock
	changes []Change
	quota   Quota
	idSeq   int64
}

// NewMemFS creates a new in-memory remote filesystem.
func NewMemFS(opts ...MemFSOption) *MemFS {
	m := &MemFS{
		files: make(map[string]RemoteObject),
		clock: RealClock{},
		quota: Quota{Used: 0, Total: 15 * 1024 * 1024 * 1024}, // 15 GB default
	}
	for _, opt := range opts {
		opt(m)
	}
	return m
}

// Seed adds a file to the in-memory filesystem. Used by tests to
// pre-populate remote state.
func (m *MemFS) Seed(remotePath string, mtime time.Time, md5 string) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.idSeq++
	m.files[remotePath] = RemoteObject{
		Path:     remotePath,
		Size:     1024, // default seed size
		ModTime:  mtime,
		MD5:      md5,
		RemoteID: fmt.Sprintf("id-%d", m.idSeq),
	}
}

// SeedWithSize adds a file with a specific size.
func (m *MemFS) SeedWithSize(remotePath string, mtime time.Time, md5 string, size int64) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.idSeq++
	m.files[remotePath] = RemoteObject{
		Path:     remotePath,
		Size:     size,
		ModTime:  mtime,
		MD5:      md5,
		RemoteID: fmt.Sprintf("id-%d", m.idSeq),
	}
}

// SetQuota sets the quota values returned by Quota().
func (m *MemFS) SetQuota(used, total int64) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.quota = Quota{Used: used, Total: total}
}

// AddChange appends a simulated remote change for ListChanges to return.
func (m *MemFS) AddChange(c Change) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.changes = append(m.changes, c)
}

// Files returns a snapshot of all files currently in the MemFS.
// Useful for test assertions.
func (m *MemFS) Files() map[string]RemoteObject {
	m.mu.Lock()
	defer m.mu.Unlock()
	out := make(map[string]RemoteObject, len(m.files))
	for k, v := range m.files {
		out[k] = v
	}
	return out
}

// CopyFile simulates uploading a local file to the remote directory.
func (m *MemFS) CopyFile(_ context.Context, src, dstDir string) (RemoteObject, error) {
	m.mu.Lock()
	defer m.mu.Unlock()

	remotePath := path.Join(dstDir, path.Base(src))
	m.idSeq++
	obj := RemoteObject{
		Path:     remotePath,
		Size:     1024,
		ModTime:  m.clock.Now(),
		MD5:      fmt.Sprintf("md5-%d", m.idSeq),
		RemoteID: fmt.Sprintf("id-%d", m.idSeq),
	}
	m.files[remotePath] = obj
	m.quota.Used += obj.Size
	return obj, nil
}

// DeleteFile removes the file at the given remote path.
func (m *MemFS) DeleteFile(_ context.Context, remotePath string) error {
	m.mu.Lock()
	defer m.mu.Unlock()

	obj, ok := m.files[remotePath]
	if !ok {
		return fmt.Errorf("delete %q: %w", remotePath, ErrNotFound)
	}
	m.quota.Used -= obj.Size
	delete(m.files, remotePath)
	return nil
}

// MoveFile renames a remote file from src to dst. This is O(1) matching
// Google Drive semantics where a move is a metadata-only operation.
func (m *MemFS) MoveFile(_ context.Context, src, dst string) error {
	m.mu.Lock()
	defer m.mu.Unlock()

	obj, ok := m.files[src]
	if !ok {
		return fmt.Errorf("move %q: %w", src, ErrNotFound)
	}
	if _, exists := m.files[dst]; exists {
		return fmt.Errorf("move to %q: %w", dst, ErrAlreadyExists)
	}
	obj.Path = dst
	m.files[dst] = obj
	delete(m.files, src)
	return nil
}

// Stat returns metadata for the remote object at path.
func (m *MemFS) Stat(_ context.Context, remotePath string) (RemoteObject, error) {
	m.mu.Lock()
	defer m.mu.Unlock()

	obj, ok := m.files[remotePath]
	if !ok {
		return RemoteObject{}, fmt.Errorf("stat %q: %w", remotePath, ErrNotFound)
	}
	return obj, nil
}

// ListChanges returns simulated changes. The pageToken is an opaque index
// into the changes slice. Pass "" to start from the beginning.
func (m *MemFS) ListChanges(_ context.Context, pageToken string) (Changes, error) {
	m.mu.Lock()
	defer m.mu.Unlock()

	var startIdx int64
	if pageToken != "" {
		if _, err := fmt.Sscanf(pageToken, "%d", &startIdx); err != nil {
			return Changes{}, fmt.Errorf("invalid page token %q: %w", pageToken, err)
		}
	}

	if startIdx >= int64(len(m.changes)) {
		return Changes{
			Items:         nil,
			NextPageToken: fmt.Sprintf("%d", len(m.changes)),
		}, nil
	}

	items := m.changes[startIdx:]
	return Changes{
		Items:         items,
		NextPageToken: fmt.Sprintf("%d", len(m.changes)),
	}, nil
}

// Quota returns the current simulated storage usage.
func (m *MemFS) Quota(_ context.Context) (Quota, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.quota, nil
}
