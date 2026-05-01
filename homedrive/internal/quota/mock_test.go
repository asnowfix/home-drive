package quota

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"sync"
	"testing"
)

// --- test doubles ---

// mockRemoteFS implements RemoteFS for testing.
type mockRemoteFS struct {
	mu    sync.Mutex
	quota QuotaInfo
	err   error
}

func (m *mockRemoteFS) Quota(_ context.Context) (QuotaInfo, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.quota, m.err
}

func (m *mockRemoteFS) SetQuota(used, total int64) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.quota = QuotaInfo{Used: used, Total: total}
}

func (m *mockRemoteFS) SetError(err error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.err = err
}

// mockPublisher implements Publisher for testing.
type mockPublisher struct {
	mu     sync.Mutex
	events []publishedEvent
}

type publishedEvent struct {
	Topic   string
	Payload map[string]any
}

func (p *mockPublisher) PublishJSON(topic string, payload any) error {
	p.mu.Lock()
	defer p.mu.Unlock()

	// Marshal and unmarshal to normalize the payload to map[string]any.
	b, err := json.Marshal(payload)
	if err != nil {
		return fmt.Errorf("mock publish marshal: %w", err)
	}
	var m map[string]any
	if err := json.Unmarshal(b, &m); err != nil {
		return fmt.Errorf("mock publish unmarshal: %w", err)
	}
	p.events = append(p.events, publishedEvent{Topic: topic, Payload: m})
	return nil
}

func (p *mockPublisher) Topic(parts ...string) string {
	result := "homedrive/testhost/testuser"
	for _, part := range parts {
		result += "/" + part
	}
	return result
}

func (p *mockPublisher) Events() []publishedEvent {
	p.mu.Lock()
	defer p.mu.Unlock()
	cp := make([]publishedEvent, len(p.events))
	copy(cp, p.events)
	return cp
}

func (p *mockPublisher) Reset() {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.events = nil
}

// mockPushController implements PushController for testing.
type mockPushController struct {
	mu       sync.Mutex
	paused   bool
	pauseCt  int
	resumeCt int
}

func (c *mockPushController) PausePush() {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.paused = true
	c.pauseCt++
}

func (c *mockPushController) ResumePush() {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.paused = false
	c.resumeCt++
}

func (c *mockPushController) IsPaused() bool {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.paused
}

func (c *mockPushController) PauseCount() int {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.pauseCt
}

func (c *mockPushController) ResumeCount() int {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.resumeCt
}

// --- helpers ---

func newTestMonitor(t *testing.T, remote *mockRemoteFS, pub *mockPublisher, push *mockPushController, dryRun bool) *Monitor {
	t.Helper()
	cfg := DefaultConfig()
	cfg.DryRun = dryRun
	log := slog.Default()
	return NewMonitor(remote, pub, push, cfg, log)
}
