package desktop

import (
	"context"
	"sync"
	"time"
)

// fakeMount records the order of lifecycle calls so tests can assert that
// uploads are flushed BEFORE the unmount (rclone v1.76.0 does not do that
// itself: nothing in cmd/ calls VFS.WaitForWriters).
type fakeMount struct {
	mu    sync.Mutex
	calls []string

	busy          bool          // Busy() result until WaitForWriters drains it
	drains        bool          // WaitForWriters empties the queue
	waitBlocks    bool          // WaitForWriters never returns (until released)
	unmountErrs   []error       // consumed one per Unmount call
	lazyErr       error         // LazyUnmount result
	done          chan error    // Done()
	closeDoneOnUm bool          // Unmount ends the server loop
	release       chan struct{} // unblocks WaitForWriters
}

func newFakeMount() *fakeMount {
	return &fakeMount{done: make(chan error, 1), drains: true, closeDoneOnUm: true, release: make(chan struct{})}
}

func (f *fakeMount) rec(s string) {
	f.mu.Lock()
	f.calls = append(f.calls, s)
	f.mu.Unlock()
}

func (f *fakeMount) Calls() []string {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]string(nil), f.calls...)
}

func (f *fakeMount) Busy() bool {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.busy
}

func (f *fakeMount) WaitForWriters(timeout time.Duration) {
	f.rec("wait")
	if f.waitBlocks {
		<-f.release
		return
	}
	if f.drains {
		f.mu.Lock()
		f.busy = false
		f.mu.Unlock()
	}
}

func (f *fakeMount) Unmount() error {
	f.rec("unmount")
	f.mu.Lock()
	var err error
	if len(f.unmountErrs) > 0 {
		err = f.unmountErrs[0]
		f.unmountErrs = f.unmountErrs[1:]
	}
	f.mu.Unlock()
	if err == nil && f.closeDoneOnUm {
		f.done <- nil
	}
	return err
}

func (f *fakeMount) LazyUnmount() error {
	f.rec("lazy")
	if f.lazyErr == nil && f.closeDoneOnUm {
		f.done <- nil
	}
	return f.lazyErr
}

func (f *fakeMount) Close()             { f.rec("close") }
func (f *fakeMount) Done() <-chan error { return f.done }

// fakeMounter returns a fixed fakeMount and records the Spec it was given.
type fakeMounter struct {
	m    *fakeMount
	err  error
	spec Spec
}

func (f *fakeMounter) Mount(_ context.Context, spec Spec) (Mount, error) {
	f.spec = spec
	if f.err != nil {
		return nil, f.err
	}
	return f.m, nil
}

type fakeNotifier struct {
	mu     sync.Mutex
	events []string
}

func (n *fakeNotifier) Ready() error    { n.add("ready"); return nil }
func (n *fakeNotifier) Stopping() error { n.add("stopping"); return nil }
func (n *fakeNotifier) add(s string) {
	n.mu.Lock()
	n.events = append(n.events, s)
	n.mu.Unlock()
}
func (n *fakeNotifier) Events() []string {
	n.mu.Lock()
	defer n.mu.Unlock()
	return append([]string(nil), n.events...)
}
