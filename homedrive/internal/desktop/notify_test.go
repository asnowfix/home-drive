package desktop

import (
	"net"
	"path/filepath"
	"testing"
	"time"
)

func TestNewNotifier_Cases(t *testing.T) {
	if _, ok := NewNotifier(testEnv(nil)).(NopNotifier); !ok {
		t.Fatal("unset NOTIFY_SOCKET must give a NopNotifier")
	}
	n := NewNotifier(testEnv(map[string]string{"NOTIFY_SOCKET": "/x"}))
	if _, ok := n.(socketNotifier); !ok {
		t.Fatalf("got %T", n)
	}
	var nop NopNotifier
	if nop.Ready() != nil || nop.Stopping() != nil {
		t.Fatal("NopNotifier must not fail")
	}
}

func TestSocketNotifier_SendsStates(t *testing.T) {
	sock := filepath.Join(t.TempDir(), "notify.sock")
	conn, err := net.ListenPacket("unixgram", sock)
	if err != nil {
		t.Skipf("unix datagram sockets unavailable: %v", err)
	}
	defer func() { _ = conn.Close() }()

	n := NewNotifier(testEnv(map[string]string{"NOTIFY_SOCKET": sock}))
	tests := []struct {
		name string
		send func() error
		want string
	}{
		{"Ready", n.Ready, "READY=1"},
		{"Stopping", n.Stopping, "STOPPING=1"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			if err := tc.send(); err != nil {
				t.Fatal(err)
			}
			buf := make([]byte, 64)
			_ = conn.SetReadDeadline(time.Now().Add(2 * time.Second))
			k, _, err := conn.ReadFrom(buf)
			if err != nil {
				t.Fatal(err)
			}
			if got := string(buf[:k]); got != tc.want {
				t.Fatalf("datagram = %q, want %q", got, tc.want)
			}
		})
	}
}

func TestSocketNotifier_NoListener(t *testing.T) {
	n := socketNotifier{socket: filepath.Join(t.TempDir(), "nobody.sock")}
	if err := n.Ready(); err == nil {
		t.Fatal("sending to a missing socket must error")
	}
}
