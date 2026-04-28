package mqtt

import (
	"fmt"
	"log/slog"
	"time"
)

// EventType enumerates the event types published on the events topic.
type EventType string

const (
	EventPushSuccess        EventType = "push.success"
	EventPushFailure        EventType = "push.failure"
	EventPullSuccess        EventType = "pull.success"
	EventPullFailure        EventType = "pull.failure"
	EventConflictDetected   EventType = "conflict.detected"
	EventConflictResolved   EventType = "conflict.resolved"
	EventDirRename          EventType = "dir_rename"
	EventQuotaWarning       EventType = "quota.warning"
	EventQuotaExhausted     EventType = "quota.exhausted"
	EventOAuthRefreshFailed EventType = "oauth.refresh_failed"
)

// AllEventTypes returns all supported event types.
func AllEventTypes() []EventType {
	return []EventType{
		EventPushSuccess,
		EventPushFailure,
		EventPullSuccess,
		EventPullFailure,
		EventConflictDetected,
		EventConflictResolved,
		EventDirRename,
		EventQuotaWarning,
		EventQuotaExhausted,
		EventOAuthRefreshFailed,
	}
}

// Event is the JSON payload published to the events topic. Fields beyond
// Timestamp and Type are optional and depend on the event type.
type Event struct {
	Timestamp   string    `json:"ts"`
	Type        EventType `json:"type"`
	Path        string    `json:"path,omitempty"`
	LocalMtime  string    `json:"local_mtime,omitempty"`
	RemoteMtime string    `json:"remote_mtime,omitempty"`
	Resolution  string    `json:"resolution,omitempty"`
	KeptOldAs   string    `json:"kept_old_as,omitempty"`
	From        string    `json:"from,omitempty"`
	To          string    `json:"to,omitempty"`
	FilesCount  int       `json:"files_count,omitempty"`
	Bytes       int64     `json:"bytes,omitempty"`
	Error       string    `json:"error,omitempty"`
	QuotaPct    float64   `json:"quota_pct,omitempty"`
}

// EventPublisher publishes typed events to MQTT. Events are published
// with QoS 1 and retain=false, matching the PLAN.md section 9.2 spec.
type EventPublisher struct {
	pub Publisher
	qos byte
	log *slog.Logger
}

// NewEventPublisher creates an EventPublisher wrapping the given Publisher.
func NewEventPublisher(pub Publisher, qos byte, log *slog.Logger) *EventPublisher {
	if qos == 0 {
		qos = 1
	}
	return &EventPublisher{
		pub: pub,
		qos: qos,
		log: log,
	}
}

// Emit publishes an event to homedrive/<host>/<user>/events/<type>.
// The Timestamp field is set automatically if empty.
func (ep *EventPublisher) Emit(evt Event) error {
	if evt.Timestamp == "" {
		evt.Timestamp = time.Now().UTC().Format(time.RFC3339)
	}
	if evt.Type == "" {
		return fmt.Errorf("event type is required")
	}

	topic := ep.pub.Topic("events", string(evt.Type))
	if err := ep.pub.PublishJSON(topic, evt); err != nil {
		ep.log.Error("event publish failed",
			"type", string(evt.Type),
			"topic", topic,
			"error", err,
		)
		return fmt.Errorf("event publish %s: %w", evt.Type, err)
	}

	ep.log.Debug("event published",
		"type", string(evt.Type),
		"topic", topic,
	)
	return nil
}

// EmitSync publishes a push or pull success/failure event.
func (ep *EventPublisher) EmitSync(eventType EventType, path string, bytes int64, err error) error {
	evt := Event{
		Type:  eventType,
		Path:  path,
		Bytes: bytes,
	}
	if err != nil {
		evt.Error = err.Error()
	}
	return ep.Emit(evt)
}

// EmitConflict publishes conflict.detected or conflict.resolved events.
func (ep *EventPublisher) EmitConflict(
	eventType EventType,
	path string,
	localMtime, remoteMtime time.Time,
	resolution, keptOldAs string,
) error {
	evt := Event{
		Type:       eventType,
		Path:       path,
		Resolution: resolution,
		KeptOldAs:  keptOldAs,
	}
	if !localMtime.IsZero() {
		evt.LocalMtime = localMtime.UTC().Format(time.RFC3339)
	}
	if !remoteMtime.IsZero() {
		evt.RemoteMtime = remoteMtime.UTC().Format(time.RFC3339)
	}
	return ep.Emit(evt)
}

// EmitDirRename publishes a dir_rename event.
func (ep *EventPublisher) EmitDirRename(from, to string, filesCount int) error {
	return ep.Emit(Event{
		Type:       EventDirRename,
		From:       from,
		To:         to,
		FilesCount: filesCount,
	})
}

// EmitQuota publishes a quota.warning or quota.exhausted event.
func (ep *EventPublisher) EmitQuota(eventType EventType, quotaPct float64) error {
	return ep.Emit(Event{
		Type:     eventType,
		QuotaPct: quotaPct,
	})
}

// EmitOAuthFailure publishes an oauth.refresh_failed event.
func (ep *EventPublisher) EmitOAuthFailure(errMsg string) error {
	return ep.Emit(Event{
		Type:  EventOAuthRefreshFailed,
		Error: errMsg,
	})
}
