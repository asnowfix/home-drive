package rcloneclient

import (
	"context"
	"sync"
	"time"
)

// FlakyAction describes a failure to inject into a RemoteFS call.
type FlakyAction struct {
	// Err is returned if non-nil.
	Err error

	// Delay is added before the call proceeds (simulates latency).
	Delay time.Duration

	// Timeout, if true, makes the call block until the context is cancelled.
	Timeout bool
}

// FlakyRule describes when and how to inject a failure.
type FlakyRule struct {
	// Method is the name of the RemoteFS method to match.
	// Use "*" to match all methods.
	Method string

	// Match, if non-nil, further filters whether the rule applies.
	// The first argument is the method name; the second is the first
	// string argument (e.g. path or src).
	Match func(method, firstArg string) bool

	// Action describes the failure to inject.
	Action FlakyAction
}

// FlakyFS wraps a RemoteFS and injects failures according to configured rules.
// Rules are evaluated in order; the first matching rule is applied.
type FlakyFS struct {
	inner RemoteFS
	mu    sync.Mutex
	rules []FlakyRule
}

// NewFlakyFS creates a FlakyFS decorating the given inner RemoteFS.
func NewFlakyFS(inner RemoteFS, rules ...FlakyRule) *FlakyFS {
	return &FlakyFS{
		inner: inner,
		rules: rules,
	}
}

// SetRules replaces the current rule set. Thread-safe.
func (f *FlakyFS) SetRules(rules ...FlakyRule) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.rules = rules
}

// ClearRules removes all rules, making FlakyFS pass through.
func (f *FlakyFS) ClearRules() {
	f.SetRules()
}

// applyRule checks rules and applies the first match. Returns true if a
// rule was applied and the caller should return the given error.
func (f *FlakyFS) applyRule(ctx context.Context, method, firstArg string) error {
	f.mu.Lock()
	rules := make([]FlakyRule, len(f.rules))
	copy(rules, f.rules)
	f.mu.Unlock()

	for _, r := range rules {
		if r.Method != "*" && r.Method != method {
			continue
		}
		if r.Match != nil && !r.Match(method, firstArg) {
			continue
		}
		return f.executeAction(ctx, r.Action)
	}
	return nil
}

// executeAction applies the flaky action (delay, timeout, or error).
func (f *FlakyFS) executeAction(ctx context.Context, a FlakyAction) error {
	if a.Timeout {
		<-ctx.Done()
		return ctx.Err()
	}
	if a.Delay > 0 {
		select {
		case <-time.After(a.Delay):
		case <-ctx.Done():
			return ctx.Err()
		}
	}
	return a.Err
}

// CopyFile implements RemoteFS.
func (f *FlakyFS) CopyFile(ctx context.Context, src, dstDir string) (RemoteObject, error) {
	if err := f.applyRule(ctx, "CopyFile", src); err != nil {
		return RemoteObject{}, err
	}
	return f.inner.CopyFile(ctx, src, dstDir)
}

// DeleteFile implements RemoteFS.
func (f *FlakyFS) DeleteFile(ctx context.Context, path string) error {
	if err := f.applyRule(ctx, "DeleteFile", path); err != nil {
		return err
	}
	return f.inner.DeleteFile(ctx, path)
}

// MoveFile implements RemoteFS.
func (f *FlakyFS) MoveFile(ctx context.Context, src, dst string) error {
	if err := f.applyRule(ctx, "MoveFile", src); err != nil {
		return err
	}
	return f.inner.MoveFile(ctx, src, dst)
}

// Stat implements RemoteFS.
func (f *FlakyFS) Stat(ctx context.Context, path string) (RemoteObject, error) {
	if err := f.applyRule(ctx, "Stat", path); err != nil {
		return RemoteObject{}, err
	}
	return f.inner.Stat(ctx, path)
}

// ListChanges implements RemoteFS.
func (f *FlakyFS) ListChanges(ctx context.Context, pageToken string) (Changes, error) {
	if err := f.applyRule(ctx, "ListChanges", pageToken); err != nil {
		return Changes{}, err
	}
	return f.inner.ListChanges(ctx, pageToken)
}

// Quota implements RemoteFS.
func (f *FlakyFS) Quota(ctx context.Context) (Quota, error) {
	if err := f.applyRule(ctx, "Quota", ""); err != nil {
		return Quota{}, err
	}
	return f.inner.Quota(ctx)
}
