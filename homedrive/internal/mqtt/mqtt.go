// Package mqtt wraps eclipse/paho.mqtt.golang behind a stable Publisher
// interface for Home Assistant discovery, state publishing, and event
// reporting. Designed for future extension to cross-device peer sync.
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

// Config holds MQTT connection and topic configuration.
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
	PublishInterval   time.Duration // default 30s, for periodic state publishing
}

// Publisher is the public interface for MQTT publishing. All packages
// outside internal/mqtt use this interface, never paho directly.
type Publisher interface {
	// Publish sends a message to the given topic.
	Publish(topic string, qos byte, retain bool, payload any) error
	// PublishJSON marshals payload as JSON and publishes with config QoS.
	PublishJSON(topic string, payload any) error
	// Topic builds a full topic path: <base>/<host>/<user>/<parts...>.
	Topic(parts ...string) string
	// Close gracefully disconnects from the broker.
	Close(ctx context.Context) error
}

// Client implements Publisher using the paho MQTT library.
type Client struct {
	cfg    Config
	host   string
	user   string
	log    *slog.Logger
	client paho.Client

	mu        sync.Mutex
	closed    atomic.Bool
	errCount  atomic.Int64
	connected atomic.Bool
}

// New creates a new MQTT Client and connects to the broker. The LWT is
// configured to publish "offline" to the online topic on unexpected
// disconnect.
func New(cfg Config, host, user string, log *slog.Logger) (*Client, error) {
	if cfg.QoS == 0 {
		cfg.QoS = 1
	}
	if cfg.KeepAlive == 0 {
		cfg.KeepAlive = 30 * time.Second
	}
	if cfg.ReconnectMax == 0 {
		cfg.ReconnectMax = 5 * time.Minute
	}
	if cfg.PublishInterval == 0 {
		cfg.PublishInterval = 30 * time.Second
	}

	c := &Client{
		cfg:  cfg,
		host: host,
		user: user,
		log:  log,
	}

	clientID := fmt.Sprintf("%s_%s_%s", cfg.ClientIDPrefix, host, user)
	onlineTopic := c.Topic("online")

	opts := paho.NewClientOptions().
		AddBroker(cfg.Broker).
		SetClientID(clientID).
		SetKeepAlive(cfg.KeepAlive).
		SetMaxReconnectInterval(cfg.ReconnectMax).
		SetAutoReconnect(true).
		SetCleanSession(true).
		SetWill(onlineTopic, "offline", cfg.QoS, true).
		SetOnConnectHandler(func(_ paho.Client) {
			c.connected.Store(true)
			c.log.Info("mqtt connected", "broker", cfg.Broker)
			// Publish online status after connect.
			if token := c.client.Publish(onlineTopic, cfg.QoS, true, "online"); token.Wait() && token.Error() != nil {
				c.log.Error("mqtt publish online failed", "error", token.Error())
			}
		}).
		SetConnectionLostHandler(func(_ paho.Client, err error) {
			c.connected.Store(false)
			c.log.Warn("mqtt connection lost", "error", err)
		})

	if cfg.Username != "" {
		opts.SetUsername(cfg.Username)
		opts.SetPassword(cfg.Password)
	}

	c.client = paho.NewClient(opts)
	token := c.client.Connect()
	token.Wait()
	if err := token.Error(); err != nil {
		return nil, fmt.Errorf("mqtt connect to %s: %w", cfg.Broker, err)
	}

	return c, nil
}

// Publish sends a raw payload to the given topic.
func (c *Client) Publish(topic string, qos byte, retain bool, payload any) error {
	if c.closed.Load() {
		return fmt.Errorf("mqtt client closed")
	}
	token := c.client.Publish(topic, qos, retain, payload)
	token.Wait()
	if err := token.Error(); err != nil {
		c.errCount.Add(1)
		c.log.Error("mqtt publish failed", "topic", topic, "error", err)
		return fmt.Errorf("mqtt publish to %s: %w", topic, err)
	}
	return nil
}

// PublishJSON marshals the payload as JSON and publishes with the
// configured QoS and no retain flag.
func (c *Client) PublishJSON(topic string, payload any) error {
	data, err := json.Marshal(payload)
	if err != nil {
		return fmt.Errorf("mqtt json marshal: %w", err)
	}
	return c.Publish(topic, c.cfg.QoS, false, data)
}

// Topic builds a full topic path: <base>/<host>/<user>/<parts...>.
func (c *Client) Topic(parts ...string) string {
	prefix := strings.Join([]string{c.cfg.BaseTopic, c.host, c.user}, "/")
	if len(parts) == 0 {
		return prefix
	}
	return prefix + "/" + strings.Join(parts, "/")
}

// Close gracefully disconnects, first publishing "offline" status.
func (c *Client) Close(_ context.Context) error {
	if c.closed.Swap(true) {
		return nil
	}

	onlineTopic := c.Topic("online")
	if token := c.client.Publish(onlineTopic, c.cfg.QoS, true, "offline"); token.Wait() && token.Error() != nil {
		c.log.Error("mqtt publish offline failed", "error", token.Error())
	}

	c.client.Disconnect(250)
	c.log.Info("mqtt disconnected")
	return nil
}

// ErrorCount returns the cumulative publish error count (useful for metrics).
func (c *Client) ErrorCount() int64 {
	return c.errCount.Load()
}

// IsConnected returns whether the client believes it is connected.
func (c *Client) IsConnected() bool {
	return c.connected.Load()
}

// Future extension interfaces — NOT implemented in v0.1.
// Their presence is a design contract for cross-device peer sync.
//
// type Subscriber interface {
//     Subscribe(topic string, qos byte, handler MessageHandler) error
//     Unsubscribe(topic string) error
// }
//
// type PeerCoordinator interface {
//     AnnouncePresence(ctx context.Context) error
//     Peers(ctx context.Context) ([]Peer, error)
//     AcquireLock(ctx context.Context, key string) (Lock, error)
//     ProposeConflictResolution(ctx context.Context, ...) (Decision, error)
// }
