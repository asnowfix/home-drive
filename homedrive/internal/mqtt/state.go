package mqtt

import (
	"context"
	"fmt"
	"log/slog"
	"strconv"
	"time"
)

// StatusProvider supplies the current agent status. Implemented by the
// syncer or a top-level coordinator. Defined here so this package
// compiles independently.
type StatusProvider interface {
	// Status returns the current agent status string:
	// "running", "paused", "error", or "quota_blocked".
	Status() string
	// LastPush returns the timestamp of the last successful push, or zero.
	LastPush() time.Time
	// LastPull returns the timestamp of the last successful pull, or zero.
	LastPull() time.Time
}

// MetricsProvider supplies numeric metrics for state publishing.
// Defined here so this package compiles independently.
type MetricsProvider interface {
	// PendingUploads returns the number of items waiting to be pushed.
	PendingUploads() int
	// PendingDownloads returns the number of items waiting to be pulled.
	PendingDownloads() int
	// Conflicts24h returns the number of conflicts detected in the last 24h.
	Conflicts24h() int
	// BytesUploaded24h returns bytes uploaded in the last 24h.
	BytesUploaded24h() int64
	// BytesDownloaded24h returns bytes downloaded in the last 24h.
	BytesDownloaded24h() int64
	// QuotaUsedPct returns the Drive quota usage as a percentage (0-100).
	QuotaUsedPct() float64
}

// StatePublisher periodically reads state from providers and publishes
// it to the corresponding MQTT topics.
type StatePublisher struct {
	pub      Publisher
	status   StatusProvider
	metrics  MetricsProvider
	interval time.Duration
	log      *slog.Logger
}

// NewStatePublisher creates a StatePublisher that publishes at the given
// interval. Use Run to start the publish loop.
func NewStatePublisher(
	pub Publisher,
	status StatusProvider,
	metrics MetricsProvider,
	interval time.Duration,
	log *slog.Logger,
) *StatePublisher {
	if interval == 0 {
		interval = 30 * time.Second
	}
	return &StatePublisher{
		pub:      pub,
		status:   status,
		metrics:  metrics,
		interval: interval,
		log:      log,
	}
}

// Run starts the periodic state publishing loop. It blocks until the
// context is cancelled. It publishes immediately on start, then at
// the configured interval.
func (sp *StatePublisher) Run(ctx context.Context) {
	sp.publishState()

	ticker := time.NewTicker(sp.interval)
	defer ticker.Stop()

	for {
		select {
		case <-ctx.Done():
			sp.log.Info("state publisher stopped")
			return
		case <-ticker.C:
			sp.publishState()
		}
	}
}

// PublishOnce publishes the current state a single time. Useful for
// on-demand publishing (e.g. after /reload).
func (sp *StatePublisher) PublishOnce() {
	sp.publishState()
}

// publishState reads from providers and publishes to all state topics.
func (sp *StatePublisher) publishState() {
	sp.publishString("status", sp.status.Status())
	sp.publishTimestamp("last_push", sp.status.LastPush())
	sp.publishTimestamp("last_pull", sp.status.LastPull())
	sp.publishInt("queue/pending_up", sp.metrics.PendingUploads())
	sp.publishInt("queue/pending_down", sp.metrics.PendingDownloads())
	sp.publishInt("conflicts_24h", sp.metrics.Conflicts24h())
	sp.publishInt64("bytes_up_24h", sp.metrics.BytesUploaded24h())
	sp.publishInt64("bytes_down_24h", sp.metrics.BytesDownloaded24h())
	sp.publishFloat("quota_used_pct", sp.metrics.QuotaUsedPct())
}

func (sp *StatePublisher) publishString(subtopic, value string) {
	topic := sp.pub.Topic(subtopic)
	if err := sp.pub.Publish(topic, 1, true, value); err != nil {
		sp.log.Error("state publish failed", "topic", subtopic, "error", err)
	}
}

func (sp *StatePublisher) publishTimestamp(subtopic string, t time.Time) {
	value := ""
	if !t.IsZero() {
		value = t.UTC().Format(time.RFC3339)
	}
	sp.publishString(subtopic, value)
}

func (sp *StatePublisher) publishInt(subtopic string, value int) {
	sp.publishString(subtopic, strconv.Itoa(value))
}

func (sp *StatePublisher) publishInt64(subtopic string, value int64) {
	sp.publishString(subtopic, strconv.FormatInt(value, 10))
}

func (sp *StatePublisher) publishFloat(subtopic string, value float64) {
	sp.publishString(subtopic, fmt.Sprintf("%.1f", value))
}
