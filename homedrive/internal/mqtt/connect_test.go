package mqtt

import (
	"context"
	"net"
	"testing"
	"time"

	paho "github.com/eclipse/paho.mqtt.golang"
)

// freeAddr returns a currently unused 127.0.0.1:port.
func freeAddr(t *testing.T) string {
	t.Helper()
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("listen: %v", err)
	}
	addr := ln.Addr().String()
	_ = ln.Close()
	return addr
}

func TestNew_BrokerDownAtStart(t *testing.T) {
	tests := []struct {
		name     string
		publish  int
		wantDrop int64
	}{
		{"no_publish", 0, 0},
		{"publishes_dropped_not_queued", 5, 5},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			cfg := testConfig("tcp://" + freeAddr(t))
			client, err := New(cfg, "myhost", "myuser", testLogger())
			if err != nil {
				t.Fatalf("New must not fail with broker down: %v", err)
			}
			t.Cleanup(func() { _ = client.Close(context.Background()) })

			if client.Connected() {
				t.Fatal("client must not report connected")
			}
			for i := 0; i < tc.publish; i++ {
				if err := client.PublishJSON(client.Topic("status"), map[string]int{"i": i}); err != nil {
					t.Fatalf("publish must not error while disconnected: %v", err)
				}
			}
			if got := client.DroppedCount(); got != tc.wantDrop {
				t.Errorf("dropped = %d, want %d", got, tc.wantDrop)
			}
			if got := client.ErrorCount(); got != 0 {
				t.Errorf("error count = %d, want 0 (no spam)", got)
			}
		})
	}
}

func TestNew_BrokerComesUpLater(t *testing.T) {
	addr := freeAddr(t)
	client, err := New(testConfig("tcp://"+addr), "myhost", "myuser", testLogger())
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	t.Cleanup(func() { _ = client.Close(context.Background()) })
	if client.Connected() {
		t.Fatal("must start disconnected")
	}

	srv := newBrokerOnAddr(t, addr)
	if srv == nil {
		t.Skip("could not rebind broker port")
	}
	t.Cleanup(func() { _ = srv.Close() })

	deadline := time.Now().Add(15 * time.Second)
	for !client.Connected() && time.Now().Before(deadline) {
		time.Sleep(50 * time.Millisecond)
	}
	if !client.Connected() {
		t.Fatal("client never connected after the broker came up")
	}
	msgs := make(chan string, 1)
	sub := paho.NewClient(paho.NewClientOptions().AddBroker("tcp://" + addr).
		SetClientID("test-sub-late").SetAutoReconnect(false))
	if tok := sub.Connect(); !tok.WaitTimeout(5*time.Second) || tok.Error() != nil {
		t.Fatal("subscriber connect failed")
	}
	defer sub.Disconnect(100)
	sub.Subscribe(client.Topic("online"), 1, func(_ paho.Client, m paho.Message) {
		msgs <- string(m.Payload())
	}).WaitTimeout(5 * time.Second)
	select {
	case p := <-msgs:
		if p != "online" {
			t.Errorf("online payload = %q", p)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("no retained online message after late connect")
	}
	if err := client.PublishJSON(client.Topic("status"), map[string]int{"x": 1}); err != nil {
		t.Errorf("publish after connect: %v", err)
	}
	if client.DroppedCount() != 0 {
		t.Errorf("dropped = %d, want 0", client.DroppedCount())
	}
}

func TestClose_WhileBrokerDown(t *testing.T) {
	client, err := New(testConfig("tcp://"+freeAddr(t)), "h", "u", testLogger())
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	done := make(chan struct{})
	go func() { _ = client.Close(context.Background()); close(done) }()
	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("Close blocked while broker down")
	}
}
