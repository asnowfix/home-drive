// Package mqtt wraps eclipse/paho.mqtt.golang behind a stable Publisher
// interface for Home Assistant discovery, state publishing, and event
// reporting. Designed for future extension to cross-device peer sync.
//
// # Reserved future topic namespaces (do NOT use in v0.1 publishes)
//
//   - homedrive/peers/<host>   — retained presence beacon
//   - homedrive/locks/<key>    — distributed mutex
//   - homedrive/sync/proposals/<id> — conflict resolution voting
//   - homedrive/sync/decisions/<id> — resolution outcome
package mqtt

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	paho "github.com/eclipse/paho.mqtt.golang"
)

// Config holds the broker connection and topic settings for the MQTT
// publisher. All fields map to the config.yaml mqtt section.
type Config struct {
	Broker            string        // tcp://host:1883
	ClientIDPrefix    string        // homedrive
	BaseTopic         string        // homedrive
	HADiscoveryPrefix string        // homeassistant
	QoS               byte          // 0|1|2 (default 1)
	KeepAlive         time.Duration // default 30s
	ReconnectMax      time.Duration // default 5m
	Username          string        // optional
	Password          string        // optional
}

// Publisher is the public interface for MQTT publishing. The syncer and
// HA discovery code depend on this interface, never on paho directly.
type Publisher interface {
	// Publish sends a message on the given topic. payload is serialized
	// as a byte slice ([]byte) or string; anything else is JSON-encoded.
	Publish(topic string, qos byte, retain bool, payload any) error

	// PublishJSON marshals payload to JSON and publishes with the
	// configured default QoS and retain=false.
	PublishJSON(topic string, payload any) error

	// Topic builds a fully-qualified topic path:
	//   <base>/<host>/<user>/<parts...>
	Topic(parts ...string) string

	// Close gracefully disconnects from the broker. It publishes the
	// offline LWT payload before disconnecting. The context controls
	// the maximum time to wait for in-flight messages.
	Close(ctx context.Context) error
}

// Client implements Publisher by wrapping a paho MQTT client with LWT,
// auto-reconnect, and structured logging.
type Client struct {
	cfg    Config
	host   string
	user   string
	log    *slog.Logger
	paho   paho.Client
	errors atomic.Int64

	// mu protects closed to prevent double-close races.
	mu     sync.Mutex
	closed bool
}

// New creates a new MQTT Client and connects to the broker. It
// configures LWT, auto-reconnect, and publishes the "online" retained
// message after a successful connection.
func New(cfg Config, host, user string, log *slog.Logger) (*Client, error) {
	cfg = withDefaults(cfg)

	c := &Client{
		cfg:  cfg,
		host: host,
		user: user,
		log:  log.With("component", "mqtt"),
	}

	onlineTopic := c.Topic("online")

	opts := paho.NewClientOptions().
		AddBroker(cfg.Broker).
		SetClientID(clientID(cfg.ClientIDPrefix, host, user)).
		SetKeepAlive(cfg.KeepAlive).
		SetMaxReconnectInterval(cfg.ReconnectMax).
		SetAutoReconnect(true).
		SetCleanSession(true).
		SetWill(onlineTopic, "offline", cfg.QoS, true).
		SetOnConnectHandler(c.onConnect).
		SetConnectionLostHandler(c.onConnectionLost)

	if cfg.Username != "" {
		opts.SetUsername(cfg.Username)
		opts.SetPassword(cfg.Password)
	}

	c.paho = paho.NewClient(opts)
	token := c.paho.Connect()
	if !token.WaitTimeout(10 * time.Second) {
		return nil, fmt.Errorf("mqtt connect timeout: broker=%s", cfg.Broker)
	}
	if err := token.Error(); err != nil {
		return nil, fmt.Errorf("mqtt connect: %w", err)
	}

	return c, nil
}

// Publish sends a message to the specified topic. If payload is []byte
// or string it is sent as-is; otherwise it is JSON-encoded. Publishes
// are non-blocking: errors are logged and counted but not returned to
// avoid blocking the caller on network hiccups.
func (c *Client) Publish(topic string, qos byte, retain bool, payload any) error {
	data, err := toBytes(payload)
	if err != nil {
		c.errors.Add(1)
		c.log.Error("mqtt payload encode failed",
			"topic", topic,
			"error", err,
		)
		return fmt.Errorf("mqtt payload encode: %w", err)
	}

	token := c.paho.Publish(topic, qos, retain, data)
	// Non-blocking: fire-and-forget with error accounting.
	go func() {
		if !token.WaitTimeout(5 * time.Second) {
			c.errors.Add(1)
			c.log.Warn("mqtt publish timeout", "topic", topic)
			return
		}
		if err := token.Error(); err != nil {
			c.errors.Add(1)
			c.log.Warn("mqtt publish failed",
				"topic", topic,
				"error", err,
			)
		}
	}()

	return nil
}

// PublishJSON marshals payload to JSON and publishes it with the
// configured default QoS and retain=false.
func (c *Client) PublishJSON(topic string, payload any) error {
	data, err := json.Marshal(payload)
	if err != nil {
		c.errors.Add(1)
		c.log.Error("mqtt json marshal failed",
			"topic", topic,
			"error", err,
		)
		return fmt.Errorf("mqtt json marshal: %w", err)
	}
	return c.Publish(topic, c.cfg.QoS, false, data)
}

// Topic builds a fully-qualified MQTT topic path:
//
//	<base>/<host>/<user>/<parts...>
//
// For example: Topic("events", "push.success") returns
// "homedrive/myhost/myuser/events/push.success".
func (c *Client) Topic(parts ...string) string {
	segments := make([]string, 0, 3+len(parts))
	segments = append(segments, c.cfg.BaseTopic, c.host, c.user)
	segments = append(segments, parts...)
	return strings.Join(segments, "/")
}

// Close gracefully shuts down the MQTT client. It publishes "offline"
// on the LWT topic, then disconnects. The context controls the maximum
// wait time.
func (c *Client) Close(ctx context.Context) error {
	c.mu.Lock()
	defer c.mu.Unlock()

	if c.closed {
		return nil
	}
	c.closed = true

	// Publish offline status before disconnecting, but only if the
	// connection is currently open. Attempting to publish while
	// disconnected would block indefinitely waiting for the token.
	if c.paho.IsConnectionOpen() {
		onlineTopic := c.Topic("online")
		token := c.paho.Publish(onlineTopic, c.cfg.QoS, true, []byte("offline"))
		waitForToken(ctx, token)
	}

	// Quiesce: wait up to 500ms for in-flight messages.
	c.paho.Disconnect(500)
	c.log.Info("mqtt client disconnected")
	return nil
}

// ErrorCount returns the cumulative number of publish errors since the
// client was created. Useful for metrics/monitoring.
func (c *Client) ErrorCount() int64 {
	return c.errors.Load()
}

// onConnect is the paho OnConnect callback. It publishes the "online"
// retained message after each (re)connection.
func (c *Client) onConnect(_ paho.Client) {
	c.log.Info("mqtt connected", "broker", c.cfg.Broker)
	onlineTopic := c.Topic("online")
	token := c.paho.Publish(onlineTopic, c.cfg.QoS, true, []byte("online"))
	go func() {
		if !token.WaitTimeout(5 * time.Second) {
			c.errors.Add(1)
			c.log.Warn("mqtt online publish timeout")
		}
	}()
}

// onConnectionLost is the paho OnConnectionLost callback. It logs the
// error; paho handles the exponential-backoff reconnect automatically.
func (c *Client) onConnectionLost(_ paho.Client, err error) {
	c.log.Warn("mqtt connection lost",
		"broker", c.cfg.Broker,
		"error", err,
	)
}

// withDefaults fills in zero-valued fields with sane defaults.
func withDefaults(cfg Config) Config {
	if cfg.QoS == 0 {
		cfg.QoS = 1
	}
	if cfg.KeepAlive == 0 {
		cfg.KeepAlive = 30 * time.Second
	}
	if cfg.ReconnectMax == 0 {
		cfg.ReconnectMax = 5 * time.Minute
	}
	if cfg.BaseTopic == "" {
		cfg.BaseTopic = "homedrive"
	}
	if cfg.ClientIDPrefix == "" {
		cfg.ClientIDPrefix = "homedrive"
	}
	return cfg
}

// clientID builds a unique client identifier from prefix, host, and user.
func clientID(prefix, host, user string) string {
	return prefix + "_" + host + "_" + user
}

// toBytes converts a payload to []byte for paho. Strings and byte
// slices are passed through; everything else is JSON-encoded.
func toBytes(payload any) ([]byte, error) {
	switch v := payload.(type) {
	case []byte:
		return v, nil
	case string:
		return []byte(v), nil
	default:
		return json.Marshal(v)
	}
}

// waitForToken blocks until either the token completes or the context
// is cancelled.
func waitForToken(ctx context.Context, token paho.Token) {
	select {
	case <-token.Done():
	case <-ctx.Done():
	}
}

// ---------------------------------------------------------------------------
// Future extension interfaces (NOT implemented in v0.1).
//
// These are documented here as a design contract. When cross-device sync
// lands, Client will implement these interfaces. Existing Publisher
// consumers remain unaffected.
// ---------------------------------------------------------------------------

// // Subscriber enables topic subscriptions for future peer-sync features.
// type Subscriber interface {
// 	Subscribe(topic string, qos byte, handler MessageHandler) error
// 	Unsubscribe(topic string) error
// }

// // PeerCoordinator enables cross-device coordination via MQTT for future
// // multi-NAS deployments.
// type PeerCoordinator interface {
// 	AnnouncePresence(ctx context.Context) error
// 	Peers(ctx context.Context) ([]Peer, error)
// 	AcquireLock(ctx context.Context, key string) (Lock, error)
// 	ProposeConflictResolution(ctx context.Context, proposal any) (any, error)
// }
