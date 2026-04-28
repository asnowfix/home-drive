package mqtt

import (
	"fmt"
	"net"
	"testing"

	mochi "github.com/mochi-mqtt/server/v2"
	"github.com/mochi-mqtt/server/v2/hooks/auth"
	"github.com/mochi-mqtt/server/v2/listeners"
	"github.com/mochi-mqtt/server/v2/packets"
)

// startEmbeddedBroker starts an embedded MQTT broker on an ephemeral port
// and returns the broker address (tcp://host:port). The broker is stopped
// on test cleanup.
func startEmbeddedBroker(t *testing.T) (string, *mochi.Server) {
	t.Helper()

	s := mochi.New(&mochi.Options{
		InlineClient: true,
	})

	if err := s.AddHook(new(auth.AllowHook), nil); err != nil {
		t.Fatalf("failed to add auth hook: %v", err)
	}

	// Find an ephemeral port.
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("failed to listen on ephemeral port: %v", err)
	}
	addr := ln.Addr().String()
	ln.Close()

	tcp := listeners.NewTCP(listeners.Config{
		ID:      "test",
		Address: addr,
	})
	if err := s.AddListener(tcp); err != nil {
		t.Fatalf("failed to add TCP listener: %v", err)
	}

	go func() {
		if err := s.Serve(); err != nil {
			// Serve returns error on close, which is expected during cleanup.
			_ = err
		}
	}()

	t.Cleanup(func() {
		_ = s.Close()
	})

	return fmt.Sprintf("tcp://%s", addr), s
}

// testConfig returns a Config suitable for tests with the given broker address.
func testConfig(brokerAddr string) Config {
	return Config{
		Broker:            brokerAddr,
		ClientIDPrefix:    "test",
		BaseTopic:         "homedrive",
		HADiscoveryPrefix: "homeassistant",
		QoS:               1,
	}
}

// subscribeInline subscribes via the mochi inline client and sends received
// payloads to the returned channel. This is the correct way to use mochi's
// inline subscription API.
func subscribeInline(t *testing.T, srv *mochi.Server, filter string) <-chan []byte {
	t.Helper()
	ch := make(chan []byte, 20)
	if err := srv.Subscribe(filter, 1, func(_ *mochi.Client, _ packets.Subscription, pk packets.Packet) {
		// Copy payload to avoid data races with mochi internals.
		payload := make([]byte, len(pk.Payload))
		copy(payload, pk.Payload)
		ch <- payload
	}); err != nil {
		t.Fatalf("subscribe error for %s: %v", filter, err)
	}
	return ch
}

// subscribeInlineWithTopic subscribes and includes the topic in received messages.
func subscribeInlineWithTopic(t *testing.T, srv *mochi.Server, filter string) <-chan receivedMsg {
	t.Helper()
	ch := make(chan receivedMsg, 20)
	if err := srv.Subscribe(filter, 1, func(_ *mochi.Client, _ packets.Subscription, pk packets.Packet) {
		payload := make([]byte, len(pk.Payload))
		copy(payload, pk.Payload)
		ch <- receivedMsg{
			topic:   pk.TopicName,
			payload: payload,
		}
	}); err != nil {
		t.Fatalf("subscribe error for %s: %v", filter, err)
	}
	return ch
}

type receivedMsg struct {
	topic   string
	payload []byte
}
