package mqtt

import (
	"context"
	"encoding/json"
	"errors"
	"log/slog"
	"os"
	"testing"
	"time"
)

func TestEventPublisher_Emit_SetsTimestamp(t *testing.T) {
	brokerAddr, srv := startEmbeddedBroker(t)
	log := slog.New(slog.NewJSONHandler(os.Stderr, nil))

	cfg := testConfig(brokerAddr)
	client, err := New(cfg, "evhost", "evuser", log)
	if err != nil {
		t.Fatalf("New() error: %v", err)
	}
	defer client.Close(context.Background())

	topic := client.Topic("events", string(EventPushSuccess))
	received := subscribeInline(t, srv, topic)

	ep := NewEventPublisher(client, 1, log)
	if err := ep.Emit(Event{Type: EventPushSuccess, Path: "test.txt"}); err != nil {
		t.Fatalf("Emit() error: %v", err)
	}

	select {
	case payload := <-received:
		var evt Event
		if err := json.Unmarshal(payload, &evt); err != nil {
			t.Fatalf("json.Unmarshal() error: %v", err)
		}
		if evt.Timestamp == "" {
			t.Error("expected non-empty timestamp")
		}
		if evt.Type != EventPushSuccess {
			t.Errorf("type = %q, want %q", evt.Type, EventPushSuccess)
		}
		if evt.Path != "test.txt" {
			t.Errorf("path = %q, want %q", evt.Path, "test.txt")
		}
	case <-time.After(3 * time.Second):
		t.Error("timeout waiting for event")
	}
}

func TestEventPublisher_Emit_PreservesExplicitTimestamp(t *testing.T) {
	brokerAddr, srv := startEmbeddedBroker(t)
	log := slog.New(slog.NewJSONHandler(os.Stderr, nil))

	cfg := testConfig(brokerAddr)
	client, err := New(cfg, "evhost2", "evuser2", log)
	if err != nil {
		t.Fatalf("New() error: %v", err)
	}
	defer client.Close(context.Background())

	topic := client.Topic("events", string(EventPullSuccess))
	received := subscribeInline(t, srv, topic)

	ep := NewEventPublisher(client, 1, log)
	fixedTS := "2026-04-28T14:32:11Z"
	if err := ep.Emit(Event{
		Timestamp: fixedTS,
		Type:      EventPullSuccess,
		Path:      "doc.pdf",
	}); err != nil {
		t.Fatalf("Emit() error: %v", err)
	}

	select {
	case payload := <-received:
		var evt Event
		if err := json.Unmarshal(payload, &evt); err != nil {
			t.Fatalf("json.Unmarshal() error: %v", err)
		}
		if evt.Timestamp != fixedTS {
			t.Errorf("timestamp = %q, want %q", evt.Timestamp, fixedTS)
		}
	case <-time.After(3 * time.Second):
		t.Error("timeout waiting for event")
	}
}

func TestEventPublisher_Emit_RequiresType(t *testing.T) {
	brokerAddr, _ := startEmbeddedBroker(t)
	log := slog.New(slog.NewJSONHandler(os.Stderr, nil))

	cfg := testConfig(brokerAddr)
	client, err := New(cfg, "evhost3", "evuser3", log)
	if err != nil {
		t.Fatalf("New() error: %v", err)
	}
	defer client.Close(context.Background())

	ep := NewEventPublisher(client, 1, log)
	err = ep.Emit(Event{Path: "test.txt"})
	if err == nil {
		t.Error("expected error for missing event type")
	}
}

func TestEventPublisher_EmitConflict_Payload(t *testing.T) {
	brokerAddr, srv := startEmbeddedBroker(t)
	log := slog.New(slog.NewJSONHandler(os.Stderr, nil))

	cfg := testConfig(brokerAddr)
	client, err := New(cfg, "cfhost", "cfuser", log)
	if err != nil {
		t.Fatalf("New() error: %v", err)
	}
	defer client.Close(context.Background())

	topic := client.Topic("events", string(EventConflictDetected))
	received := subscribeInline(t, srv, topic)

	ep := NewEventPublisher(client, 1, log)
	localTime := time.Date(2026, 4, 28, 14, 32, 0, 0, time.UTC)
	remoteTime := time.Date(2026, 4, 28, 14, 31, 45, 0, time.UTC)

	if err := ep.EmitConflict(
		EventConflictDetected,
		"Documents/notes.md",
		localTime, remoteTime,
		"newer_wins:local",
		"Documents/notes.md.old.3",
	); err != nil {
		t.Fatalf("EmitConflict() error: %v", err)
	}

	select {
	case payload := <-received:
		var evt Event
		if err := json.Unmarshal(payload, &evt); err != nil {
			t.Fatalf("json.Unmarshal() error: %v", err)
		}
		if evt.Type != EventConflictDetected {
			t.Errorf("type = %q, want %q", evt.Type, EventConflictDetected)
		}
		if evt.Path != "Documents/notes.md" {
			t.Errorf("path = %q, want %q", evt.Path, "Documents/notes.md")
		}
		if evt.LocalMtime != "2026-04-28T14:32:00Z" {
			t.Errorf("local_mtime = %q, want %q", evt.LocalMtime, "2026-04-28T14:32:00Z")
		}
		if evt.RemoteMtime != "2026-04-28T14:31:45Z" {
			t.Errorf("remote_mtime = %q, want %q", evt.RemoteMtime, "2026-04-28T14:31:45Z")
		}
		if evt.Resolution != "newer_wins:local" {
			t.Errorf("resolution = %q, want %q", evt.Resolution, "newer_wins:local")
		}
		if evt.KeptOldAs != "Documents/notes.md.old.3" {
			t.Errorf("kept_old_as = %q, want %q", evt.KeptOldAs, "Documents/notes.md.old.3")
		}
	case <-time.After(3 * time.Second):
		t.Error("timeout waiting for conflict event")
	}
}

func TestEventPublisher_EmitDirRename_Payload(t *testing.T) {
	brokerAddr, srv := startEmbeddedBroker(t)
	log := slog.New(slog.NewJSONHandler(os.Stderr, nil))

	cfg := testConfig(brokerAddr)
	client, err := New(cfg, "drhost", "druser", log)
	if err != nil {
		t.Fatalf("New() error: %v", err)
	}
	defer client.Close(context.Background())

	topic := client.Topic("events", string(EventDirRename))
	received := subscribeInline(t, srv, topic)

	ep := NewEventPublisher(client, 1, log)
	if err := ep.EmitDirRename("/mnt/data/old_dir", "/mnt/data/new_dir", 500); err != nil {
		t.Fatalf("EmitDirRename() error: %v", err)
	}

	select {
	case payload := <-received:
		var evt Event
		if err := json.Unmarshal(payload, &evt); err != nil {
			t.Fatalf("json.Unmarshal() error: %v", err)
		}
		if evt.Type != EventDirRename {
			t.Errorf("type = %q, want %q", evt.Type, EventDirRename)
		}
		if evt.From != "/mnt/data/old_dir" {
			t.Errorf("from = %q, want %q", evt.From, "/mnt/data/old_dir")
		}
		if evt.To != "/mnt/data/new_dir" {
			t.Errorf("to = %q, want %q", evt.To, "/mnt/data/new_dir")
		}
		if evt.FilesCount != 500 {
			t.Errorf("files_count = %d, want %d", evt.FilesCount, 500)
		}
	case <-time.After(3 * time.Second):
		t.Error("timeout waiting for dir_rename event")
	}
}

func TestEventPublisher_EmitSync_Success(t *testing.T) {
	brokerAddr, srv := startEmbeddedBroker(t)
	log := slog.New(slog.NewJSONHandler(os.Stderr, nil))

	cfg := testConfig(brokerAddr)
	client, err := New(cfg, "synhost", "synuser", log)
	if err != nil {
		t.Fatalf("New() error: %v", err)
	}
	defer client.Close(context.Background())

	topic := client.Topic("events", string(EventPushSuccess))
	received := subscribeInline(t, srv, topic)

	ep := NewEventPublisher(client, 1, log)
	if err := ep.EmitSync(EventPushSuccess, "photos/sunset.jpg", 4096, nil); err != nil {
		t.Fatalf("EmitSync() error: %v", err)
	}

	select {
	case payload := <-received:
		var evt Event
		if err := json.Unmarshal(payload, &evt); err != nil {
			t.Fatalf("json.Unmarshal() error: %v", err)
		}
		if evt.Bytes != 4096 {
			t.Errorf("bytes = %d, want %d", evt.Bytes, 4096)
		}
		if evt.Error != "" {
			t.Errorf("error should be empty, got %q", evt.Error)
		}
	case <-time.After(3 * time.Second):
		t.Error("timeout")
	}
}

func TestEventPublisher_EmitSync_Failure(t *testing.T) {
	brokerAddr, srv := startEmbeddedBroker(t)
	log := slog.New(slog.NewJSONHandler(os.Stderr, nil))

	cfg := testConfig(brokerAddr)
	client, err := New(cfg, "failhost", "failuser", log)
	if err != nil {
		t.Fatalf("New() error: %v", err)
	}
	defer client.Close(context.Background())

	topic := client.Topic("events", string(EventPushFailure))
	received := subscribeInline(t, srv, topic)

	ep := NewEventPublisher(client, 1, log)
	syncErr := errors.New("network timeout")
	if err := ep.EmitSync(EventPushFailure, "photos/sunset.jpg", 0, syncErr); err != nil {
		t.Fatalf("EmitSync() error: %v", err)
	}

	select {
	case payload := <-received:
		var evt Event
		if err := json.Unmarshal(payload, &evt); err != nil {
			t.Fatalf("json.Unmarshal() error: %v", err)
		}
		if evt.Error != "network timeout" {
			t.Errorf("error = %q, want %q", evt.Error, "network timeout")
		}
	case <-time.After(3 * time.Second):
		t.Error("timeout")
	}
}

func TestEventPublisher_EmitQuota_Warning(t *testing.T) {
	brokerAddr, srv := startEmbeddedBroker(t)
	log := slog.New(slog.NewJSONHandler(os.Stderr, nil))

	cfg := testConfig(brokerAddr)
	client, err := New(cfg, "qhost", "quser", log)
	if err != nil {
		t.Fatalf("New() error: %v", err)
	}
	defer client.Close(context.Background())

	topic := client.Topic("events", string(EventQuotaWarning))
	received := subscribeInline(t, srv, topic)

	ep := NewEventPublisher(client, 1, log)
	if err := ep.EmitQuota(EventQuotaWarning, 92.5); err != nil {
		t.Fatalf("EmitQuota() error: %v", err)
	}

	select {
	case payload := <-received:
		var evt Event
		if err := json.Unmarshal(payload, &evt); err != nil {
			t.Fatalf("json.Unmarshal() error: %v", err)
		}
		if evt.Type != EventQuotaWarning {
			t.Errorf("type = %q, want %q", evt.Type, EventQuotaWarning)
		}
		if evt.QuotaPct != 92.5 {
			t.Errorf("quota_pct = %f, want %f", evt.QuotaPct, 92.5)
		}
	case <-time.After(3 * time.Second):
		t.Error("timeout")
	}
}

func TestEventPublisher_EmitOAuthFailure(t *testing.T) {
	brokerAddr, srv := startEmbeddedBroker(t)
	log := slog.New(slog.NewJSONHandler(os.Stderr, nil))

	cfg := testConfig(brokerAddr)
	client, err := New(cfg, "oahost", "oauser", log)
	if err != nil {
		t.Fatalf("New() error: %v", err)
	}
	defer client.Close(context.Background())

	topic := client.Topic("events", string(EventOAuthRefreshFailed))
	received := subscribeInline(t, srv, topic)

	ep := NewEventPublisher(client, 1, log)
	if err := ep.EmitOAuthFailure("refresh token expired"); err != nil {
		t.Fatalf("EmitOAuthFailure() error: %v", err)
	}

	select {
	case payload := <-received:
		var evt Event
		if err := json.Unmarshal(payload, &evt); err != nil {
			t.Fatalf("json.Unmarshal() error: %v", err)
		}
		if evt.Type != EventOAuthRefreshFailed {
			t.Errorf("type = %q, want %q", evt.Type, EventOAuthRefreshFailed)
		}
		if evt.Error != "refresh token expired" {
			t.Errorf("error = %q, want %q", evt.Error, "refresh token expired")
		}
	case <-time.After(3 * time.Second):
		t.Error("timeout")
	}
}

func TestAllEventTypes_Count(t *testing.T) {
	types := AllEventTypes()
	if got := len(types); got != 10 {
		t.Errorf("expected 10 event types, got %d", got)
	}
}

func TestEventPublisher_EventTopicFormat(t *testing.T) {
	brokerAddr, _ := startEmbeddedBroker(t)
	log := slog.New(slog.NewJSONHandler(os.Stderr, nil))

	cfg := testConfig(brokerAddr)
	client, err := New(cfg, "fmthost", "fmtuser", log)
	if err != nil {
		t.Fatalf("New() error: %v", err)
	}
	defer client.Close(context.Background())

	tests := []struct {
		eventType EventType
		wantTopic string
	}{
		{EventPushSuccess, "homedrive/fmthost/fmtuser/events/push.success"},
		{EventPullFailure, "homedrive/fmthost/fmtuser/events/pull.failure"},
		{EventConflictDetected, "homedrive/fmthost/fmtuser/events/conflict.detected"},
		{EventDirRename, "homedrive/fmthost/fmtuser/events/dir_rename"},
		{EventQuotaExhausted, "homedrive/fmthost/fmtuser/events/quota.exhausted"},
		{EventOAuthRefreshFailed, "homedrive/fmthost/fmtuser/events/oauth.refresh_failed"},
	}

	for _, tt := range tests {
		t.Run(string(tt.eventType), func(t *testing.T) {
			got := client.Topic("events", string(tt.eventType))
			if got != tt.wantTopic {
				t.Errorf("topic = %q, want %q", got, tt.wantTopic)
			}
		})
	}
}

func TestEvent_SamplePayload_MatchesPlan(t *testing.T) {
	// Verify the sample payload from PLAN.md section 9.2 can be represented.
	evt := Event{
		Timestamp:   "2026-04-28T14:32:11Z",
		Type:        EventConflictDetected,
		Path:        "Documents/notes.md",
		LocalMtime:  "2026-04-28T14:32:00Z",
		RemoteMtime: "2026-04-28T14:31:45Z",
		Resolution:  "newer_wins:local",
		KeptOldAs:   "Documents/notes.md.old.3",
	}

	data, err := json.Marshal(evt)
	if err != nil {
		t.Fatalf("json.Marshal() error: %v", err)
	}

	var decoded map[string]interface{}
	if err := json.Unmarshal(data, &decoded); err != nil {
		t.Fatalf("json.Unmarshal() error: %v", err)
	}

	// Verify all expected fields are present.
	expected := map[string]string{
		"ts":           "2026-04-28T14:32:11Z",
		"type":         "conflict.detected",
		"path":         "Documents/notes.md",
		"local_mtime":  "2026-04-28T14:32:00Z",
		"remote_mtime": "2026-04-28T14:31:45Z",
		"resolution":   "newer_wins:local",
		"kept_old_as":  "Documents/notes.md.old.3",
	}

	for key, want := range expected {
		got, ok := decoded[key]
		if !ok {
			t.Errorf("missing field %q in payload", key)
			continue
		}
		if got != want {
			t.Errorf("field %q = %v, want %v", key, got, want)
		}
	}

	// Verify zero-value fields are omitted (omitempty).
	for _, field := range []string{"from", "to", "files_count", "bytes", "error", "quota_pct"} {
		if _, ok := decoded[field]; ok {
			t.Errorf("field %q should be omitted when zero", field)
		}
	}
}
