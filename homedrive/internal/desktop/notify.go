package desktop

import (
	"fmt"
	"net"
	"strings"
)

// Notifier reports the service state to systemd (Type=notify). rclone's
// mountlib only notifies from its own cobra command (cmd/mountlib Run, via
// lib/systemd.Notify), which homedrive does not use: it calls
// MountPoint.Mount directly. So homedrive is the single notifier and cannot
// notify twice.
type Notifier interface {
	// Ready is sent once the mount is live (READY=1).
	Ready() error
	// Stopping is sent when shutdown begins (STOPPING=1).
	Stopping() error
}

// NopNotifier does nothing; it is used when NOTIFY_SOCKET is unset.
type NopNotifier struct{}

// Ready implements Notifier.
func (NopNotifier) Ready() error { return nil }

// Stopping implements Notifier.
func (NopNotifier) Stopping() error { return nil }

// socketNotifier implements the sd_notify(3) protocol: one datagram per
// state on the unix socket named by NOTIFY_SOCKET.
type socketNotifier struct{ socket string }

// NewNotifier returns a Notifier that talks to the socket named by the
// NOTIFY_SOCKET variable of env, or a NopNotifier when it is unset.
func NewNotifier(env Env) Notifier {
	sock := env.Getenv("NOTIFY_SOCKET")
	if sock == "" {
		return NopNotifier{}
	}
	return socketNotifier{socket: sock}
}

func (n socketNotifier) Ready() error    { return n.send("READY=1") }
func (n socketNotifier) Stopping() error { return n.send("STOPPING=1") }

func (n socketNotifier) send(state string) error {
	addr := n.socket
	if strings.HasPrefix(addr, "@") { // abstract socket namespace
		addr = "\x00" + addr[1:]
	}
	conn, err := net.Dial("unixgram", addr)
	if err != nil {
		return fmt.Errorf("desktop: sd_notify %s: %w", state, err)
	}
	defer func() { _ = conn.Close() }()
	if _, err := conn.Write([]byte(state)); err != nil {
		return fmt.Errorf("desktop: sd_notify %s: %w", state, err)
	}
	return nil
}
