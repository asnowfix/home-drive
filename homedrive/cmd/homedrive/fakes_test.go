package main

import (
	"context"
	"fmt"
	"os"
	"sync"
	"time"

	"github.com/asnowfix/home-drive/homedrive/internal/rcloneclient"
)

// fakeRemoteFS is a minimal thread-safe in-memory implementation of
// remoteFS, used to unit-test the Agent wiring layer without ever calling
// rclone or a real Google Drive API, per the homedrive-test-mocks skill.
// It is deliberately local to cmd/homedrive: remoteFS itself is a local
// interface (see adapters.go) shaped for what Agent needs, distinct from
// rcloneclient.RemoteFS and syncer.RemoteFS.
type fakeRemoteFS struct {
	mu        sync.Mutex
	files     map[string]rcloneclient.RemoteObject
	copyCalls []string
	moveCalls [][2]string
	quota     rcloneclient.Quota

	// copyGate, if set, blocks CopyFile (ignoring ctx, like an in-flight
	// network call) until releaseGate is called, to simulate a slow
	// remote for shutdown-drain tests. copyBlockedCh is signaled once a
	// call starts blocking. gateOnce guards against a double-close panic
	// if both the test and a t.Cleanup try to release it.
	copyGate      chan struct{}
	copyBlockedCh chan struct{}
	gateOnce      sync.Once
}

// releaseGate unblocks a call parked in CopyFile, if any. Safe to call
// multiple times and safe to call when no gate is set.
func (f *fakeRemoteFS) releaseGate() {
	f.gateOnce.Do(func() {
		if f.copyGate != nil {
			close(f.copyGate)
		}
	})
}

func newFakeRemoteFS() *fakeRemoteFS {
	return &fakeRemoteFS{
		files: make(map[string]rcloneclient.RemoteObject),
	}
}

func (f *fakeRemoteFS) CopyFile(_ context.Context, src, dstDir string) (rcloneclient.RemoteObject, error) {
	if f.copyGate != nil {
		if f.copyBlockedCh != nil {
			select {
			case f.copyBlockedCh <- struct{}{}:
			default:
			}
		}
		<-f.copyGate
	}

	data, err := os.ReadFile(src)
	if err != nil {
		return rcloneclient.RemoteObject{}, fmt.Errorf("fakeRemoteFS: read %s: %w", src, err)
	}

	f.mu.Lock()
	defer f.mu.Unlock()
	f.copyCalls = append(f.copyCalls, src)
	obj := rcloneclient.RemoteObject{
		Path:    dstDir,
		Size:    int64(len(data)),
		ModTime: time.Now(),
	}
	f.files[src] = obj
	return obj, nil
}

func (f *fakeRemoteFS) DeleteFile(_ context.Context, path string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	delete(f.files, path)
	return nil
}

func (f *fakeRemoteFS) MoveFile(_ context.Context, src, dst string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.moveCalls = append(f.moveCalls, [2]string{src, dst})
	if obj, ok := f.files[src]; ok {
		delete(f.files, src)
		f.files[dst] = obj
	}
	return nil
}

func (f *fakeRemoteFS) Stat(_ context.Context, path string) (rcloneclient.RemoteObject, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	obj, ok := f.files[path]
	if !ok {
		return rcloneclient.RemoteObject{}, os.ErrNotExist
	}
	return obj, nil
}

func (f *fakeRemoteFS) List(_ context.Context, _ string) ([]rcloneclient.RemoteObject, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	out := make([]rcloneclient.RemoteObject, 0, len(f.files))
	for _, obj := range f.files {
		out = append(out, obj)
	}
	return out, nil
}

func (f *fakeRemoteFS) ListChanges(_ context.Context, _ string) (rcloneclient.Changes, error) {
	return rcloneclient.Changes{}, nil
}

func (f *fakeRemoteFS) GetStartPageToken(_ context.Context) (string, error) {
	return "start-token", nil
}

func (f *fakeRemoteFS) DownloadFile(_ context.Context, remotePath, localPath string) error {
	return os.WriteFile(localPath, []byte("content-of-"+remotePath), 0o644)
}

func (f *fakeRemoteFS) Quota(_ context.Context) (rcloneclient.Quota, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.quota, nil
}

func (f *fakeRemoteFS) CopyCount() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return len(f.copyCalls)
}

func (f *fakeRemoteFS) HasCopiedFrom(src string) bool {
	f.mu.Lock()
	defer f.mu.Unlock()
	for _, c := range f.copyCalls {
		if c == src {
			return true
		}
	}
	return false
}

// fakeRemoteFSWithChanges wraps fakeRemoteFS (by pointer, to avoid copying
// its embedded sync.Mutex) and overrides ListChanges to return a change
// with a non-nil Object, exercising the branch in
// rcloneSyncerAdapter.ListChanges that fakeRemoteFS's default empty
// response never reaches.
type fakeRemoteFSWithChanges struct {
	*fakeRemoteFS
}

func (f *fakeRemoteFSWithChanges) ListChanges(_ context.Context, _ string) (rcloneclient.Changes, error) {
	return rcloneclient.Changes{
		Items: []rcloneclient.Change{
			{Path: "obj.txt", Object: &rcloneclient.RemoteObject{Path: "obj.txt"}},
		},
	}, nil
}

// fakeRemoteFSWithOAuthStatus wraps fakeRemoteFS (by pointer, to avoid
// copying its embedded sync.Mutex) and adds an OAuthStatus method, letting
// tests exercise rcloneHealth's optional-capability type assertion
// (issue #67) without fakeRemoteFS itself needing to implement it -- most
// tests have no reason to care about OAuth status at all.
type fakeRemoteFSWithOAuthStatus struct {
	*fakeRemoteFS
	status rcloneclient.OAuthStatus
}

func (f *fakeRemoteFSWithOAuthStatus) OAuthStatus() rcloneclient.OAuthStatus {
	return f.status
}

// fakeRemoteFSWithOAuthClientMissing wraps fakeRemoteFS (by pointer, same
// reason as above) and overrides ListChanges to always fail as
// rcloneclient.ErrOAuthClientMissing, letting tests drive
// Agent.runPullLoop -- the actual shipped pull loop, not syncer.Puller.Run
// -- through a sustained OAuth "no client_id" outage (issue #67 PR review:
// a test of Run() in isolation is not evidence about what runPullLoop
// does). It records the wall-clock time of every call so a test can
// assert the real gap between calls grows, not just that some internal
// counter advances.
type fakeRemoteFSWithOAuthClientMissing struct {
	*fakeRemoteFS

	mu    sync.Mutex
	calls []time.Time
}

func (f *fakeRemoteFSWithOAuthClientMissing) ListChanges(_ context.Context, _ string) (rcloneclient.Changes, error) {
	f.mu.Lock()
	f.calls = append(f.calls, time.Now())
	f.mu.Unlock()
	return rcloneclient.Changes{}, fmt.Errorf("fake changes.list: %w", rcloneclient.ErrOAuthClientMissing)
}

// callTimes returns a copy of the recorded call timestamps, safe to read
// after the loop under test has stopped.
func (f *fakeRemoteFSWithOAuthClientMissing) callTimes() []time.Time {
	f.mu.Lock()
	defer f.mu.Unlock()
	out := make([]time.Time, len(f.calls))
	copy(out, f.calls)
	return out
}
