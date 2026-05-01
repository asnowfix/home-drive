package mqtt

import (
	"context"
	"log/slog"
	"os"
	"sync"
	"testing"
	"time"
)

// mockStatusProvider implements StatusProvider for tests.
type mockStatusProvider struct {
	mu       sync.Mutex
	status   string
	lastPush time.Time
	lastPull time.Time
}

func (m *mockStatusProvider) Status() string {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.status
}

func (m *mockStatusProvider) LastPush() time.Time {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.lastPush
}

func (m *mockStatusProvider) LastPull() time.Time {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.lastPull
}

// mockMetricsProvider implements MetricsProvider for tests.
type mockMetricsProvider struct {
	mu           sync.Mutex
	pendingUp    int
	pendingDown  int
	conflicts24h int
	bytesUp24h   int64
	bytesDown24h int64
	quotaUsedPct float64
}

func (m *mockMetricsProvider) PendingUploads() int {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.pendingUp
}

func (m *mockMetricsProvider) PendingDownloads() int {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.pendingDown
}

func (m *mockMetricsProvider) Conflicts24h() int {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.conflicts24h
}

func (m *mockMetricsProvider) BytesUploaded24h() int64 {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.bytesUp24h
}

func (m *mockMetricsProvider) BytesDownloaded24h() int64 {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.bytesDown24h
}

func (m *mockMetricsProvider) QuotaUsedPct() float64 {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.quotaUsedPct
}

func TestStatePublisher_PublishesOnStart(t *testing.T) {
	brokerAddr, srv := startEmbeddedBroker(t)
	log := slog.New(slog.NewJSONHandler(os.Stderr, nil))

	cfg := testConfig(brokerAddr)
	client, err := New(cfg, "sthost", "stuser", log)
	if err != nil {
		t.Fatalf("New() error: %v", err)
	}
	defer client.Close(context.Background())

	status := &mockStatusProvider{
		status:   "running",
		lastPush: time.Date(2026, 4, 28, 14, 0, 0, 0, time.UTC),
		lastPull: time.Date(2026, 4, 28, 14, 1, 0, 0, time.UTC),
	}
	metrics := &mockMetricsProvider{
		pendingUp:    5,
		pendingDown:  3,
		conflicts24h: 1,
		bytesUp24h:   1024000,
		bytesDown24h: 2048000,
		quotaUsedPct: 42.5,
	}

	// Subscribe to state topics to verify publishing.
	type topicVal struct {
		mu    sync.Mutex
		value string
	}
	received := make(map[string]*topicVal)
	topics := []string{
		"status", "last_push", "last_pull",
		"queue/pending_up", "queue/pending_down",
		"conflicts_24h", "bytes_up_24h", "bytes_down_24h",
		"quota_used_pct",
	}
	for _, sub := range topics {
		fullTopic := client.Topic(sub)
		tv := &topicVal{}
		received[fullTopic] = tv
		ch := subscribeInline(t, srv, fullTopic)
		// Drain into the map in a goroutine.
		go func(ft string, tv *topicVal, ch <-chan []byte) {
			for payload := range ch {
				tv.mu.Lock()
				tv.value = string(payload)
				tv.mu.Unlock()
			}
		}(fullTopic, tv, ch)
	}

	sp := NewStatePublisher(client, status, metrics, 10*time.Second, log)

	// Run in background with a short-lived context.
	ctx, cancel := context.WithTimeout(context.Background(), 500*time.Millisecond)
	defer cancel()
	go sp.Run(ctx)

	// Wait for first publish cycle.
	time.Sleep(400 * time.Millisecond)

	// Verify key state values were published.
	tests := []struct {
		subtopic string
		want     string
	}{
		{"status", "running"},
		{"last_push", "2026-04-28T14:00:00Z"},
		{"last_pull", "2026-04-28T14:01:00Z"},
		{"queue/pending_up", "5"},
		{"queue/pending_down", "3"},
		{"conflicts_24h", "1"},
		{"bytes_up_24h", "1024000"},
		{"bytes_down_24h", "2048000"},
		{"quota_used_pct", "42.5"},
	}

	for _, tt := range tests {
		t.Run(tt.subtopic, func(t *testing.T) {
			fullTopic := client.Topic(tt.subtopic)
			tv, ok := received[fullTopic]
			if !ok {
				t.Errorf("no subscription for %s", tt.subtopic)
				return
			}
			tv.mu.Lock()
			got := tv.value
			tv.mu.Unlock()
			if got != tt.want {
				t.Errorf("topic %s: got %q, want %q", tt.subtopic, got, tt.want)
			}
		})
	}
}

func TestStatePublisher_PublishesAtInterval(t *testing.T) {
	brokerAddr, srv := startEmbeddedBroker(t)
	log := slog.New(slog.NewJSONHandler(os.Stderr, nil))

	cfg := testConfig(brokerAddr)
	client, err := New(cfg, "inthost", "intuser", log)
	if err != nil {
		t.Fatalf("New() error: %v", err)
	}
	defer client.Close(context.Background())

	status := &mockStatusProvider{status: "running"}
	metrics := &mockMetricsProvider{pendingUp: 1}

	// Track publish count for a specific topic.
	statusTopic := client.Topic("status")
	ch := subscribeInline(t, srv, statusTopic)

	var publishCount int
	var mu sync.Mutex
	go func() {
		for range ch {
			mu.Lock()
			publishCount++
			mu.Unlock()
		}
	}()

	// Use a very short interval so we can observe multiple publishes quickly.
	sp := NewStatePublisher(client, status, metrics, 100*time.Millisecond, log)

	ctx, cancel := context.WithTimeout(context.Background(), 550*time.Millisecond)
	defer cancel()
	go sp.Run(ctx)

	// Wait for context to expire plus a bit.
	time.Sleep(600 * time.Millisecond)

	mu.Lock()
	count := publishCount
	mu.Unlock()

	// Should have at least 3 publishes: 1 immediate + ticks.
	if count < 3 {
		t.Errorf("expected at least 3 publishes at 100ms interval, got %d", count)
	}
}

func TestStatePublisher_EmptyTimestamp(t *testing.T) {
	brokerAddr, srv := startEmbeddedBroker(t)
	log := slog.New(slog.NewJSONHandler(os.Stderr, nil))

	cfg := testConfig(brokerAddr)
	client, err := New(cfg, "emhost", "emuser", log)
	if err != nil {
		t.Fatalf("New() error: %v", err)
	}
	defer client.Close(context.Background())

	// Zero timestamps should publish empty strings.
	status := &mockStatusProvider{status: "running"}
	metrics := &mockMetricsProvider{}

	pushCh := subscribeInline(t, srv, client.Topic("last_push"))
	pullCh := subscribeInline(t, srv, client.Topic("last_pull"))

	sp := NewStatePublisher(client, status, metrics, time.Minute, log)
	sp.PublishOnce()

	for _, tc := range []struct {
		name string
		ch   <-chan []byte
	}{
		{"last_push", pushCh},
		{"last_pull", pullCh},
	} {
		t.Run(tc.name, func(t *testing.T) {
			select {
			case payload := <-tc.ch:
				if string(payload) != "" {
					t.Errorf("got %q, want empty string for zero time", string(payload))
				}
			case <-time.After(3 * time.Second):
				t.Error("timeout waiting for empty timestamp")
			}
		})
	}
}

func TestStatePublisher_PublishOnce(t *testing.T) {
	brokerAddr, srv := startEmbeddedBroker(t)
	log := slog.New(slog.NewJSONHandler(os.Stderr, nil))

	cfg := testConfig(brokerAddr)
	client, err := New(cfg, "oncehost", "onceuser", log)
	if err != nil {
		t.Fatalf("New() error: %v", err)
	}
	defer client.Close(context.Background())

	status := &mockStatusProvider{status: "paused"}
	metrics := &mockMetricsProvider{pendingUp: 10}

	received := subscribeInline(t, srv, client.Topic("status"))

	sp := NewStatePublisher(client, status, metrics, time.Minute, log)
	sp.PublishOnce()

	select {
	case msg := <-received:
		if string(msg) != "paused" {
			t.Errorf("got %q, want %q", string(msg), "paused")
		}
	case <-time.After(3 * time.Second):
		t.Error("timeout waiting for PublishOnce message")
	}
}
