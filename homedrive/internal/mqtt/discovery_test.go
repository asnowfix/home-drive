package mqtt

import (
	"context"
	"encoding/json"
	"log/slog"
	"os"
	"testing"
	"time"

	paho "github.com/eclipse/paho.mqtt.golang"
)

func TestEntities_Count(t *testing.T) {
	entities := Entities()
	if got := len(entities); got != 10 {
		t.Errorf("expected 10 entities, got %d", got)
	}
}

func TestEntities_AllHaveCorrectComponentType(t *testing.T) {
	tests := []struct {
		key       string
		component HAComponent
	}{
		{"status", HASensor},
		{"last_push", HASensor},
		{"last_pull", HASensor},
		{"pending_up", HASensor},
		{"pending_down", HASensor},
		{"conflicts_24h", HASensor},
		{"bytes_up_24h", HASensor},
		{"bytes_down_24h", HASensor},
		{"quota_used_pct", HASensor},
		{"online", HABinarySensor},
	}

	entities := Entities()
	entityMap := make(map[string]EntityDef)
	for _, e := range entities {
		entityMap[e.Key] = e
	}

	for _, tt := range tests {
		t.Run(tt.key, func(t *testing.T) {
			e, ok := entityMap[tt.key]
			if !ok {
				t.Fatalf("entity %q not found", tt.key)
			}
			if e.Component != tt.component {
				t.Errorf("entity %q: component = %q, want %q",
					tt.key, e.Component, tt.component)
			}
		})
	}
}

func TestEntities_DeviceClassValues(t *testing.T) {
	tests := []struct {
		key         string
		deviceClass string
	}{
		{"status", ""},
		{"last_push", "timestamp"},
		{"last_pull", "timestamp"},
		{"pending_up", ""},
		{"pending_down", ""},
		{"conflicts_24h", ""},
		{"bytes_up_24h", ""},
		{"bytes_down_24h", ""},
		{"quota_used_pct", "data_size"},
		{"online", "connectivity"},
	}

	entities := Entities()
	entityMap := make(map[string]EntityDef)
	for _, e := range entities {
		entityMap[e.Key] = e
	}

	for _, tt := range tests {
		t.Run(tt.key, func(t *testing.T) {
			e, ok := entityMap[tt.key]
			if !ok {
				t.Fatalf("entity %q not found", tt.key)
			}
			if e.DeviceClass != tt.deviceClass {
				t.Errorf("entity %q: device_class = %q, want %q",
					tt.key, e.DeviceClass, tt.deviceClass)
			}
		})
	}
}

func TestBuildDeviceBlock(t *testing.T) {
	device := buildDeviceBlock("pihost", "fix", "0.1.0")

	if len(device.Identifiers) != 1 || device.Identifiers[0] != "homedrive_pihost_fix" {
		t.Errorf("identifiers = %v, want [homedrive_pihost_fix]", device.Identifiers)
	}
	if device.Name != "homedrive (fix@pihost)" {
		t.Errorf("name = %q, want %q", device.Name, "homedrive (fix@pihost)")
	}
	if device.SWVersion != "0.1.0" {
		t.Errorf("sw_version = %q, want %q", device.SWVersion, "0.1.0")
	}
	if device.Model != "homedrive" {
		t.Errorf("model = %q, want %q", device.Model, "homedrive")
	}
	if device.Manufacturer != "asnowfix/home-automation" {
		t.Errorf("manufacturer = %q, want %q", device.Manufacturer, "asnowfix/home-automation")
	}
}

func TestDiscoveryTopic_Format(t *testing.T) {
	entity := EntityDef{
		Key:       "status",
		Component: HASensor,
	}
	got := discoveryTopic("homeassistant", "pihost", "fix", entity)
	want := "homeassistant/sensor/homedrive_pihost_fix_status/config"
	if got != want {
		t.Errorf("discoveryTopic() = %q, want %q", got, want)
	}
}

func TestDiscoveryTopic_BinarySensor(t *testing.T) {
	entity := EntityDef{
		Key:       "online",
		Component: HABinarySensor,
	}
	got := discoveryTopic("homeassistant", "pihost", "fix", entity)
	want := "homeassistant/binary_sensor/homedrive_pihost_fix_online/config"
	if got != want {
		t.Errorf("discoveryTopic() = %q, want %q", got, want)
	}
}

func TestBuildDiscoveryPayload_ContainsDeviceBlock(t *testing.T) {
	device := buildDeviceBlock("pihost", "fix", "0.1.0")
	entity := Entities()[0] // status

	payload, err := buildDiscoveryPayload(entity, device, "homedrive/pihost/fix/status")
	if err != nil {
		t.Fatalf("buildDiscoveryPayload() error: %v", err)
	}

	var cfg DiscoveryConfig
	if err := json.Unmarshal(payload, &cfg); err != nil {
		t.Fatalf("json.Unmarshal() error: %v", err)
	}

	if cfg.Device.Model != "homedrive" {
		t.Errorf("device.model = %q, want %q", cfg.Device.Model, "homedrive")
	}
	if cfg.Device.Manufacturer != "asnowfix/home-automation" {
		t.Errorf("device.manufacturer = %q, want %q", cfg.Device.Manufacturer, "asnowfix/home-automation")
	}
	if len(cfg.Device.Identifiers) != 1 || cfg.Device.Identifiers[0] != "homedrive_pihost_fix" {
		t.Errorf("device.identifiers = %v, want [homedrive_pihost_fix]", cfg.Device.Identifiers)
	}
	if cfg.StateTopic != "homedrive/pihost/fix/status" {
		t.Errorf("state_topic = %q, want %q", cfg.StateTopic, "homedrive/pihost/fix/status")
	}
}

func TestBuildDiscoveryPayload_BinarySensorFields(t *testing.T) {
	device := buildDeviceBlock("pihost", "fix", "0.1.0")
	// Find the online entity.
	var entity EntityDef
	for _, e := range Entities() {
		if e.Key == "online" {
			entity = e
			break
		}
	}

	payload, err := buildDiscoveryPayload(entity, device, "homedrive/pihost/fix/online")
	if err != nil {
		t.Fatalf("buildDiscoveryPayload() error: %v", err)
	}

	var cfg DiscoveryConfig
	if err := json.Unmarshal(payload, &cfg); err != nil {
		t.Fatalf("json.Unmarshal() error: %v", err)
	}

	if cfg.PayloadOn != "online" {
		t.Errorf("payload_on = %q, want %q", cfg.PayloadOn, "online")
	}
	if cfg.PayloadOff != "offline" {
		t.Errorf("payload_off = %q, want %q", cfg.PayloadOff, "offline")
	}
	if cfg.DeviceClass != "connectivity" {
		t.Errorf("device_class = %q, want %q", cfg.DeviceClass, "connectivity")
	}
}

func TestPublishDiscovery_PublishesAllEntities(t *testing.T) {
	brokerAddr, srv := startEmbeddedBroker(t)
	log := slog.New(slog.NewJSONHandler(os.Stderr, nil))

	cfg := testConfig(brokerAddr)

	// Subscribe to all discovery topics using a wildcard.
	received := subscribeInlineWithTopic(t, srv, "homeassistant/#")

	client, err := New(cfg, "testhost", "testuser", log)
	if err != nil {
		t.Fatalf("New() error: %v", err)
	}
	defer client.Close(context.Background())

	if err := PublishDiscovery(client, cfg, "testhost", "testuser", "0.1.0", log); err != nil {
		t.Fatalf("PublishDiscovery() error: %v", err)
	}

	// Collect messages.
	time.Sleep(500 * time.Millisecond)

	topics := make(map[string][]byte)
	for {
		select {
		case msg := <-received:
			topics[msg.topic] = msg.payload
		default:
			goto done
		}
	}
done:

	entities := Entities()
	for _, entity := range entities {
		topic := discoveryTopic("homeassistant", "testhost", "testuser", entity)
		payload, ok := topics[topic]
		if !ok {
			t.Errorf("missing discovery message for entity %q on topic %s", entity.Key, topic)
			continue
		}
		// Verify each payload contains the device block.
		var dcfg DiscoveryConfig
		if err := json.Unmarshal(payload, &dcfg); err != nil {
			t.Errorf("invalid JSON in discovery payload for %s: %v", entity.Key, err)
			continue
		}
		if dcfg.Device.Model != "homedrive" {
			t.Errorf("device.model in %s = %q, want %q", entity.Key, dcfg.Device.Model, "homedrive")
		}
	}
}

func TestPublishDiscovery_RetainTrue(t *testing.T) {
	// This test verifies that discovery messages are published with retain=true
	// by subscribing with a NEW paho client AFTER the publish and checking
	// if the retained message arrives.
	brokerAddr, _ := startEmbeddedBroker(t)
	log := slog.New(slog.NewJSONHandler(os.Stderr, nil))

	cfg := testConfig(brokerAddr)
	client, err := New(cfg, "rethost", "retuser", log)
	if err != nil {
		t.Fatalf("New() error: %v", err)
	}
	defer client.Close(context.Background())

	// Publish discovery messages first.
	if err := PublishDiscovery(client, cfg, "rethost", "retuser", "0.1.0", log); err != nil {
		t.Fatalf("PublishDiscovery() error: %v", err)
	}

	// Wait for messages to settle in the broker.
	time.Sleep(500 * time.Millisecond)

	// Now create a NEW paho client that subscribes to a discovery topic.
	// If the message was retained, this new subscriber should receive it.
	opts := paho.NewClientOptions().
		AddBroker(brokerAddr).
		SetClientID("retain_verifier").
		SetCleanSession(true)
	verifier := paho.NewClient(opts)
	token := verifier.Connect()
	token.Wait()
	if err := token.Error(); err != nil {
		t.Fatalf("verifier connect error: %v", err)
	}
	defer verifier.Disconnect(100)

	entity := Entities()[0] // status
	topic := discoveryTopic("homeassistant", "rethost", "retuser", entity)
	rcvd := make(chan []byte, 1)
	subToken := verifier.Subscribe(topic, 1, func(_ paho.Client, msg paho.Message) {
		rcvd <- msg.Payload()
	})
	subToken.Wait()
	if err := subToken.Error(); err != nil {
		t.Fatalf("subscribe error: %v", err)
	}

	select {
	case payload := <-rcvd:
		var dcfg DiscoveryConfig
		if err := json.Unmarshal(payload, &dcfg); err != nil {
			t.Fatalf("invalid retained JSON: %v", err)
		}
		if dcfg.Name != "Status" {
			t.Errorf("retained message name = %q, want %q", dcfg.Name, "Status")
		}
	case <-time.After(3 * time.Second):
		t.Error("timeout waiting for retained discovery message (retain=true not working)")
	}
}
