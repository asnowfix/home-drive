package mqtt

import (
	"log/slog"
	"time"

	"github.com/asnowfix/home-drive/homedrive/pkg/homedrive"
)

// DefaultPublishInterval is used when the configured interval is <= 0, so
// publish_interval_s in the document is always > 0.
const DefaultPublishInterval = 30 * time.Second

// SnapshotFunc builds the current status document. The publisher stamps
// Schema and PublishIntervalS itself; everything else comes from here.
type SnapshotFunc func() homedrive.StatusV2

// StatePublisher periodically publishes ONE retained JSON status document
// (schema 2.1, status-v2.md §5.3) on <base>/<host>/<user>/status.
type StatePublisher struct {
	pub      Publisher
	snapshot SnapshotFunc
	interval time.Duration
	log      *slog.Logger
}

// NewStatePublisher creates a StatePublisher publishing every interval
// (DefaultPublishInterval when <= 0). The agent drives it with PublishOnce (cmd/homedrive/status_v2.go).
func NewStatePublisher(
	pub Publisher,
	snapshot SnapshotFunc,
	interval time.Duration,
	log *slog.Logger,
) *StatePublisher {
	if interval <= 0 {
		interval = DefaultPublishInterval
	}
	return &StatePublisher{pub: pub, snapshot: snapshot, interval: interval, log: log}
}

// PublishOnce publishes the current status a single time (start, error
// exit, /reload).
func (sp *StatePublisher) PublishOnce() {
	doc := sp.snapshot()
	doc.Schema = homedrive.StatusSchema
	doc.PublishIntervalS = int(sp.interval / time.Second)
	if doc.PublishIntervalS <= 0 {
		doc.PublishIntervalS = 1
	}
	if err := sp.pub.Publish(sp.pub.Topic("status"), 1, true, doc); err != nil {
		sp.log.Error("status publish failed", "op", "status_publish", "error", err)
	}
}
