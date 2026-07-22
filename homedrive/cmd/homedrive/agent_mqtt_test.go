package main

import (
	"context"
	"io"
	"log/slog"
	"testing"
	"time"

	mqttserver "github.com/mochi-mqtt/server/v2"
	"github.com/mochi-mqtt/server/v2/hooks/auth"
	"github.com/mochi-mqtt/server/v2/listeners"

	"github.com/asnowfix/home-drive/homedrive/internal/config"
)

// startTestBroker starts an embedded mochi-mqtt broker on an ephemeral
// port for testing buildPublisher's real-MQTT path, per the
// homedrive-test-mocks skill (never a real broker in tests).
func startTestBroker(t *testing.T) string {
	t.Helper()

	srv := mqttserver.New(&mqttserver.Options{
		Logger: slog.New(slog.NewTextHandler(io.Discard, nil)),
	})
	if err := srv.AddHook(new(auth.AllowHook), nil); err != nil {
		t.Fatalf("add auth hook: %v", err)
	}
	tcp := listeners.NewTCP(listeners.Config{ID: "test", Address: "127.0.0.1:0"})
	if err := srv.AddListener(tcp); err != nil {
		t.Fatalf("add listener: %v", err)
	}
	go func() { _ = srv.Serve() }()
	t.Cleanup(func() { _ = srv.Close() })

	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		if l, ok := srv.Listeners.Get("test"); ok && l.Address() != "" {
			return "tcp://" + l.Address()
		}
		time.Sleep(5 * time.Millisecond)
	}
	t.Fatal("embedded broker did not start listening in time")
	return ""
}

// TestBuildPublisher_MQTTEnabled_ConnectsRealClient proves the fix for
// issue #49: when mqtt.enabled is true, buildPublisher must return a real,
// connected mqtt.Client instead of always defaulting to noopPublisher.
func TestBuildPublisher_MQTTEnabled_ConnectsRealClient(t *testing.T) {
	broker := startTestBroker(t)
	cfg := &config.Config{
		MQTT: config.MQTTConfig{
			Enabled:        true,
			Broker:         broker,
			ClientIDPrefix: "test",
			BaseTopic:      "homedrive",
			QoS:            1,
		},
	}

	pub, client, err := buildPublisher(cfg, slog.Default())
	if err != nil {
		t.Fatalf("buildPublisher: %v", err)
	}
	t.Cleanup(func() {
		ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
		defer cancel()
		_ = client.Close(ctx)
	})

	if client == nil {
		t.Fatal("expected a real *mqtt.Client, got nil")
	}
	if pub == nil {
		t.Fatal("expected a non-nil Publisher")
	}
	if !client.Connected() {
		t.Error("expected the client to be connected to the embedded broker")
	}

	// The publisher handed to push/pull/bisync must be the same real
	// client, not a noop -- publishing must not error.
	if err := pub.PublishJSON(pub.Topic("events", "test"), map[string]string{"x": "y"}); err != nil {
		t.Errorf("publish via wired publisher failed: %v", err)
	}
}

// TestBuildPublisher_MQTTDisabled_ReturnsNoop proves the disabled path is
// unchanged: no client is constructed and no connection is attempted.
func TestBuildPublisher_MQTTDisabled_ReturnsNoop(t *testing.T) {
	cfg := &config.Config{MQTT: config.MQTTConfig{Enabled: false}}

	pub, client, err := buildPublisher(cfg, slog.Default())
	if err != nil {
		t.Fatalf("buildPublisher: %v", err)
	}
	if client != nil {
		t.Error("expected no mqtt.Client when disabled")
	}
	if _, ok := pub.(noopPublisher); !ok {
		t.Errorf("expected noopPublisher, got %T", pub)
	}
}

// TestAgent_Healthz_IncludesMQTTWhenEnabled proves Healthz adds and
// correctly reports the mqtt component once a real client is wired.
func TestAgent_Healthz_IncludesMQTTWhenEnabled(t *testing.T) {
	broker := startTestBroker(t)
	cfg := &config.Config{MQTT: config.MQTTConfig{Enabled: true, Broker: broker, BaseTopic: "homedrive"}}

	_, client, err := buildPublisher(cfg, slog.Default())
	if err != nil {
		t.Fatalf("buildPublisher: %v", err)
	}
	t.Cleanup(func() {
		ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
		defer cancel()
		_ = client.Close(ctx)
	})

	j := newTestJournal(t)
	a := &Agent{log: slog.Default(), journal: j, rfs: newFakeRemoteFS(), mqttReal: client}

	result, err := a.Healthz(context.Background())
	if err != nil {
		t.Fatalf("healthz: %v", err)
	}
	if !result.Healthy {
		t.Errorf("expected overall healthy=true, got components %+v", result.Components)
	}
	if len(result.Components) != 3 {
		t.Fatalf("expected 3 components (store, rclone, mqtt), got %d", len(result.Components))
	}
	found := false
	for _, c := range result.Components {
		if c.Name == "mqtt" {
			found = true
			if !c.Healthy {
				t.Errorf("expected mqtt component healthy, got %+v", c)
			}
		}
	}
	if !found {
		t.Error("expected an mqtt component in the health result")
	}
}

// TestAgent_ShutdownMQTT_ClosesRealClient covers Agent.shutdownMQTT's real
// (non-nil) path: it must disconnect the client without error.
func TestAgent_ShutdownMQTT_ClosesRealClient(t *testing.T) {
	broker := startTestBroker(t)
	cfg := &config.Config{MQTT: config.MQTTConfig{Enabled: true, Broker: broker, BaseTopic: "homedrive"}}

	_, client, err := buildPublisher(cfg, slog.Default())
	if err != nil {
		t.Fatalf("buildPublisher: %v", err)
	}

	a := &Agent{log: slog.Default(), mqttReal: client}
	a.shutdownMQTT() // must not panic or hang

	if client.Connected() {
		t.Error("expected the client to be disconnected after shutdownMQTT")
	}
}

// TestAgent_ShutdownMQTT_NilIsNoop covers the disabled-MQTT path.
func TestAgent_ShutdownMQTT_NilIsNoop(t *testing.T) {
	a := &Agent{log: slog.Default()}
	a.shutdownMQTT() // must not panic
}
