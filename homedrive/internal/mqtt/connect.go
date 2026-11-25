package mqtt

import (
	"time"

	paho "github.com/eclipse/paho.mqtt.golang"
)

const (
	// initialConnectWait is how long New waits for the first connect
	// before handing control back and retrying in the background.
	initialConnectWait = 3 * time.Second
	// connectAttemptTimeout bounds one dial so a connect token always
	// completes (paho's default is 30s).
	connectAttemptTimeout = 5 * time.Second
	// connectBackoffStart is the delay after the first failed attempt.
	connectBackoffStart = time.Second
)

// startConnect makes the first connect attempt. If it does not succeed
// within initialConnectWait the broker is considered unreachable: one
// warning is logged and connectLoop retries in the background. It never
// fails the caller: MQTT is publish-only, sync must not depend on it.
func (c *Client) startConnect() {
	tok := c.paho.Connect()
	if tok.WaitTimeout(initialConnectWait) && tok.Error() == nil {
		c.connectedOnce.Store(true)
		return
	}
	c.log.Warn("mqtt broker unreachable at start; retrying in background",
		"broker", c.cfg.Broker,
		"error", tokenErr(tok),
	)
	go c.connectLoop(tok)
}

// connectLoop waits for the in-flight attempt, then retries with
// exponential backoff capped at cfg.ReconnectMax until connected or
// Close. Further failures are not logged (no spam); the success is, by
// onConnect. Once connected, paho's own auto-reconnect takes over.
func (c *Client) connectLoop(tok paho.Token) {
	backoff := connectBackoffStart
	for {
		select {
		case <-tok.Done():
		case <-c.done:
			return
		}
		if tok.Error() == nil {
			c.connectedOnce.Store(true)
			return
		}

		timer := time.NewTimer(backoff)
		select {
		case <-timer.C:
		case <-c.done:
			timer.Stop()
			return
		}
		if backoff *= 2; backoff > c.cfg.ReconnectMax {
			backoff = c.cfg.ReconnectMax
		}

		c.mu.Lock()
		if c.closed {
			c.mu.Unlock()
			return
		}
		tok = c.paho.Connect()
		c.mu.Unlock()
	}
}

// tokenErr returns the token error, or nil while the attempt is pending.
func tokenErr(tok paho.Token) error {
	select {
	case <-tok.Done():
		return tok.Error()
	default:
		return nil
	}
}

// DroppedCount returns how many publishes were discarded because the
// client had never connected to the broker.
func (c *Client) DroppedCount() int64 {
	return c.dropped.Load()
}
