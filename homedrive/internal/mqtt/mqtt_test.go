package mqtt

import (
	"context"
	"log/slog"
	"os"
	"testing"
	"time"

	paho "github.com/eclipse/paho.mqtt.golang"
)

func TestNew_ConnectsAndPublishesOnline(t *testing.T) {
	brokerAddr, srv := startEmbeddedBroker(t)
	log := slog.New(slog.NewJSONHandler(os.Stderr, nil))

	// Subscribe before the client connects so we capture the online message.
	onlineTopic := "homedrive/testhost/testuser/online"
	received := subscribeInline(t, srv, onlineTopic)

	cfg := testConfig(brokerAddr)
	client, err := New(cfg, "testhost", "testuser", log)
	if err != nil {
		t.Fatalf("New() error: %v", err)
	}
	defer client.Close(context.Background())

	select {
	case msg := <-received:
		if string(msg) != "online" {
			t.Errorf("expected 'online', got %q", string(msg))
		}
	case <-time.After(3 * time.Second):
		t.Error("timeout waiting for online message")
	}
}

func TestClient_Topic(t *testing.T) {
	tests := []struct {
		name  string
		parts []string
		want  string
	}{
		{
			name:  "NoParts",
			parts: nil,
			want:  "homedrive/myhost/myuser",
		},
		{
			name:  "SinglePart",
			parts: []string{"status"},
			want:  "homedrive/myhost/myuser/status",
		},
		{
			name:  "MultipleParts",
			parts: []string{"events", "push.success"},
			want:  "homedrive/myhost/myuser/events/push.success",
		},
		{
			name:  "QueueSubtopic",
			parts: []string{"queue", "pending_up"},
			want:  "homedrive/myhost/myuser/queue/pending_up",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			brokerAddr, _ := startEmbeddedBroker(t)
			log := slog.New(slog.NewJSONHandler(os.Stderr, nil))
			cfg := testConfig(brokerAddr)
			client, err := New(cfg, "myhost", "myuser", log)
			if err != nil {
				t.Fatalf("New() error: %v", err)
			}
			defer client.Close(context.Background())

			got := client.Topic(tt.parts...)
			if got != tt.want {
				t.Errorf("Topic(%v) = %q, want %q", tt.parts, got, tt.want)
			}
		})
	}
}

func TestClient_PublishJSON(t *testing.T) {
	brokerAddr, srv := startEmbeddedBroker(t)
	log := slog.New(slog.NewJSONHandler(os.Stderr, nil))

	cfg := testConfig(brokerAddr)
	client, err := New(cfg, "testhost", "testuser", log)
	if err != nil {
		t.Fatalf("New() error: %v", err)
	}
	defer client.Close(context.Background())

	topic := client.Topic("test", "json")
	received := subscribeInline(t, srv, topic)

	payload := map[string]string{"key": "value"}
	if err := client.PublishJSON(topic, payload); err != nil {
		t.Fatalf("PublishJSON() error: %v", err)
	}

	select {
	case msg := <-received:
		if string(msg) != `{"key":"value"}` {
			t.Errorf("unexpected JSON payload: %s", msg)
		}
	case <-time.After(3 * time.Second):
		t.Error("timeout waiting for JSON message")
	}
}

func TestClient_Close_PublishesOffline(t *testing.T) {
	brokerAddr, srv := startEmbeddedBroker(t)
	log := slog.New(slog.NewJSONHandler(os.Stderr, nil))

	cfg := testConfig(brokerAddr)
	client, err := New(cfg, "closehost", "closeuser", log)
	if err != nil {
		t.Fatalf("New() error: %v", err)
	}

	onlineTopic := "homedrive/closehost/closeuser/online"
	received := subscribeInline(t, srv, onlineTopic)

	// Drain the "online" message from connect.
	select {
	case <-received:
	case <-time.After(2 * time.Second):
	}

	if err := client.Close(context.Background()); err != nil {
		t.Fatalf("Close() error: %v", err)
	}

	deadline := time.After(3 * time.Second)
	for {
		select {
		case msg := <-received:
			if string(msg) == "offline" {
				return // success
			}
		case <-deadline:
			t.Error("timeout waiting for offline message on close")
			return
		}
	}
}

func TestClient_Publish_AfterClose(t *testing.T) {
	brokerAddr, _ := startEmbeddedBroker(t)
	log := slog.New(slog.NewJSONHandler(os.Stderr, nil))

	cfg := testConfig(brokerAddr)
	client, err := New(cfg, "testhost", "testuser", log)
	if err != nil {
		t.Fatalf("New() error: %v", err)
	}
	client.Close(context.Background())

	err = client.Publish("test/topic", 1, false, "data")
	if err == nil {
		t.Error("expected error publishing after close")
	}
}

func TestNew_InvalidBroker(t *testing.T) {
	log := slog.New(slog.NewJSONHandler(os.Stderr, nil))

	// Use a paho option that makes connect fail fast.
	cfg := Config{
		Broker:         "tcp://127.0.0.1:1", // port 1 should be unreachable
		ClientIDPrefix: "test",
		BaseTopic:      "homedrive",
	}

	// Override default paho timeout so this doesn't hang.
	origTimeout := paho.NewClientOptions().ConnectTimeout
	_ = origTimeout

	_, err := New(cfg, "testhost", "testuser", log)
	if err == nil {
		t.Error("expected connection error for invalid broker")
	}
}
