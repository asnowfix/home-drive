package mqtt

import (
	"context"
	"encoding/json"
	"log/slog"
	"net"
	"os"
	"sync"
	"testing"
	"time"

	"io"

	paho "github.com/eclipse/paho.mqtt.golang"
	mqttserver "github.com/mochi-mqtt/server/v2"
	"github.com/mochi-mqtt/server/v2/hooks/auth"
	"github.com/mochi-mqtt/server/v2/listeners"
)

// ---------------------------------------------------------------------------
// Test helpers: embedded broker
// ---------------------------------------------------------------------------

// startBroker starts an embedded mochi-mqtt broker on an ephemeral port
// and returns the server. The broker is stopped on test cleanup.
func startBroker(t *testing.T) *mqttserver.Server {
	t.Helper()

	srv := mqttserver.New(&mqttserver.Options{
		Logger: slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{
			Level: slog.LevelError,
		})),
	})
	if err := srv.AddHook(new(auth.AllowHook), nil); err != nil {
		t.Fatalf("add auth hook: %v", err)
	}

	tcp := listeners.NewTCP(listeners.Config{
		ID:      "test",
		Address: "127.0.0.1:0",
	})
	if err := srv.AddListener(tcp); err != nil {
		t.Fatalf("add listener: %v", err)
	}

	go func() {
		if err := srv.Serve(); err != nil {
			slog.Error("broker serve error", "error", err)
		}
	}()

	// Wait for the listener to start accepting.
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		if addr := tcp.Address(); addr != "127.0.0.1:0" && addr != "" {
			break
		}
		time.Sleep(5 * time.Millisecond)
	}

	t.Cleanup(func() { _ = srv.Close() })
	return srv
}

func brokerAddr(srv *mqttserver.Server) string {
	l, ok := srv.Listeners.Get("test")
	if !ok {
		return ""
	}
	return "tcp://" + l.Address()
}

func testConfig(broker string) Config {
	return Config{
		Broker:            broker,
		ClientIDPrefix:    "test",
		BaseTopic:         "homedrive",
		HADiscoveryPrefix: "homeassistant",
		QoS:               1,
		KeepAlive:         5 * time.Second,
		ReconnectMax:      2 * time.Second,
	}
}

func testLogger() *slog.Logger {
	return slog.New(slog.NewJSONHandler(os.Stderr, &slog.HandlerOptions{
		Level: slog.LevelDebug,
	}))
}

// newBrokerOnAddr starts a broker on a specific address, retrying for
// up to 5 seconds if the port is still in TIME_WAIT. The caller is
// responsible for closing the returned server.
func newBrokerOnAddr(t *testing.T, hostPort string) *mqttserver.Server {
	t.Helper()

	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		srv := mqttserver.New(&mqttserver.Options{
			Logger: slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{
				Level: slog.LevelError,
			})),
		})
		if err := srv.AddHook(new(auth.AllowHook), nil); err != nil {
			t.Fatalf("add auth hook: %v", err)
		}

		tcp := listeners.NewTCP(listeners.Config{
			ID:      "rebind",
			Address: hostPort,
		})
		if err := srv.AddListener(tcp); err == nil {
			go func() { _ = srv.Serve() }()
			// Wait for the broker to actually start accepting connections before
			// returning, otherwise paho may attempt to reconnect before Serve()
			// has bound the socket (races on fast native runners; not on QEMU).
			ready := time.Now().Add(2 * time.Second)
			for time.Now().Before(ready) {
				c, err := net.DialTimeout("tcp", hostPort, 100*time.Millisecond)
				if err == nil {
					_ = c.Close()
					return srv
				}
				time.Sleep(10 * time.Millisecond)
			}
			_ = srv.Close()
		}
		time.Sleep(200 * time.Millisecond)
	}

	return nil
}

// ---------------------------------------------------------------------------
// Integration tests: require an embedded broker
// ---------------------------------------------------------------------------

func TestNew_ConnectAndLWT(t *testing.T) {
	srv := startBroker(t)
	addr := brokerAddr(srv)
	if addr == "" {
		t.Fatal("broker address not available")
	}

	client, err := New(testConfig(addr), "myhost", "myuser", testLogger())
	if err != nil {
		t.Fatalf("New() error: %v", err)
	}
	t.Cleanup(func() { _ = client.Close(context.Background()) })

	if !client.paho.IsConnected() {
		t.Error("expected client to be connected")
	}

	// Verify the retained "online" message via a subscriber.
	received := make(chan string, 1)
	subOpts := paho.NewClientOptions().
		AddBroker(addr).
		SetClientID("test-sub-lwt").
		SetAutoReconnect(false)
	subClient := paho.NewClient(subOpts)
	tok := subClient.Connect()
	if !tok.WaitTimeout(5 * time.Second) {
		t.Fatal("subscriber connect timeout")
	}
	defer subClient.Disconnect(100)

	onlineTopic := client.Topic("online")
	tok = subClient.Subscribe(onlineTopic, 1, func(_ paho.Client, msg paho.Message) {
		received <- string(msg.Payload())
	})
	if !tok.WaitTimeout(5 * time.Second) {
		t.Fatal("subscribe timeout")
	}

	select {
	case payload := <-received:
		if payload != "online" {
			t.Errorf("expected retained 'online' payload, got %q", payload)
		}
	case <-time.After(3 * time.Second):
		t.Error("timed out waiting for retained 'online' message")
	}
}

func TestPublishJSON_RoundTrip(t *testing.T) {
	srv := startBroker(t)
	addr := brokerAddr(srv)

	client, err := New(testConfig(addr), "myhost", "myuser", testLogger())
	if err != nil {
		t.Fatalf("New() error: %v", err)
	}
	t.Cleanup(func() { _ = client.Close(context.Background()) })

	type event struct {
		Type string `json:"type"`
		Path string `json:"path"`
		TS   string `json:"ts"`
	}

	received := make(chan []byte, 1)
	subOpts := paho.NewClientOptions().
		AddBroker(addr).
		SetClientID("test-sub-json").
		SetAutoReconnect(false)
	subClient := paho.NewClient(subOpts)
	tok := subClient.Connect()
	if !tok.WaitTimeout(5 * time.Second) {
		t.Fatal("subscriber connect timeout")
	}
	defer subClient.Disconnect(100)

	topic := client.Topic("events", "push.success")
	tok = subClient.Subscribe(topic, 1, func(_ paho.Client, msg paho.Message) {
		received <- msg.Payload()
	})
	if !tok.WaitTimeout(5 * time.Second) {
		t.Fatal("subscribe timeout")
	}

	time.Sleep(100 * time.Millisecond) // let subscription propagate

	sent := event{
		Type: "push.success",
		Path: "Documents/notes.md",
		TS:   "2026-04-28T14:32:11Z",
	}
	if err := client.PublishJSON(topic, sent); err != nil {
		t.Fatalf("PublishJSON() error: %v", err)
	}

	select {
	case raw := <-received:
		var got event
		if err := json.Unmarshal(raw, &got); err != nil {
			t.Fatalf("unmarshal: %v", err)
		}
		if got != sent {
			t.Errorf("got %+v, want %+v", got, sent)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("timed out waiting for published JSON message")
	}
}

// brokerProxy sits between paho and a real broker. Closing the proxy
// connections simulates broker failure without calling Server.Close()
// directly, which has a TOCTOU data race in mochi-mqtt v2.7.9 between
// TCP.Serve's l.end check + goroutine spawn and TCP.Close's l.end=1 +
// ClientsWg.Wait().
type brokerProxy struct {
	mu     sync.Mutex
	ln     net.Listener
	target string
	conns  []net.Conn
}

func newBrokerProxy(t *testing.T, target string) *brokerProxy {
	t.Helper()
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("proxy listen: %v", err)
	}
	p := &brokerProxy{ln: ln, target: target}
	go p.serve()
	return p
}

func (p *brokerProxy) addr() string {
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.ln.Addr().String()
}

func (p *brokerProxy) serve() {
	for {
		client, err := p.ln.Accept()
		if err != nil {
			return
		}
		p.mu.Lock()
		target := p.target
		p.mu.Unlock()
		server, err := net.Dial("tcp", target)
		if err != nil {
			_ = client.Close()
			continue
		}
		p.mu.Lock()
		p.conns = append(p.conns, client, server)
		p.mu.Unlock()
		go func() { _, _ = io.Copy(server, client) }()
		go func() { _, _ = io.Copy(client, server) }()
	}
}

// kill closes all proxy connections, simulating network-level broker failure.
// The proxy listener itself stays closed so paho's immediate reconnect attempt
// gets "connection refused" while we restart the broker.
func (p *brokerProxy) kill() {
	p.mu.Lock()
	defer p.mu.Unlock()
	_ = p.ln.Close()
	for _, c := range p.conns {
		_ = c.Close()
	}
	p.conns = nil
}

// rebind reopens the proxy listener on the original address, forwarding to a
// new target. Call after kill() and after the replacement broker is ready.
func (p *brokerProxy) rebind(t *testing.T, newTarget string) bool {
	t.Helper()
	proxyAddr := p.ln.Addr().String()
	ln, err := net.Listen("tcp", proxyAddr)
	if err != nil {
		return false
	}
	p.mu.Lock()
	p.ln = ln
	p.target = newTarget
	p.mu.Unlock()
	go p.serve()
	return true
}

func TestAutoReconnect_AfterBrokerRestart(t *testing.T) {
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("reserve port: %v", err)
	}
	hostPort := ln.Addr().String()
	if err := ln.Close(); err != nil {
		t.Fatalf("close listener: %v", err)
	}

	srv := newBrokerOnAddr(t, hostPort)
	if srv == nil {
		t.Fatal("could not start initial broker")
	}
	// srv.Close() runs in t.Cleanup — by then the proxy has already closed
	// the broker connections so all session goroutines have exited and
	// ClientsWg.Wait() returns immediately without any race.
	t.Cleanup(func() { _ = srv.Close() })

	// paho connects to the proxy, which forwards to the real broker.
	proxy := newBrokerProxy(t, hostPort)
	t.Cleanup(proxy.kill)

	cfg := testConfig("tcp://" + proxy.addr())
	cfg.ReconnectMax = 1 * time.Second

	client, err := New(cfg, "myhost", "myuser", testLogger())
	if err != nil {
		t.Fatalf("New() error: %v", err)
	}

	if !client.paho.IsConnected() {
		t.Fatal("expected connected before broker kill")
	}

	// Kill the broker: close the proxy (no Server.Close() here — that's the
	// call that races in mochi-mqtt v2.7.9).  Paho detects the TCP drop and
	// starts auto-reconnecting; it gets "connection refused" until we rebind.
	proxy.kill()

	deadline := time.Now().Add(5 * time.Second)
	for client.paho.IsConnectionOpen() && time.Now().Before(deadline) {
		time.Sleep(50 * time.Millisecond)
	}

	// Restart broker on the same port.
	srv2 := newBrokerOnAddr(t, hostPort)
	if srv2 == nil {
		ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
		defer cancel()
		_ = client.Close(ctx)
		t.Skip("could not rebind to same port")
	}

	// Reopen the proxy pointing to the new broker. Paho's next reconnect
	// attempt lands here and flows through to srv2.
	if !proxy.rebind(t, hostPort) {
		ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
		defer cancel()
		_ = client.Close(ctx)
		_ = srv2.Close()
		t.Skip("could not rebind proxy")
	}

	deadline = time.Now().Add(10 * time.Second)
	for !client.paho.IsConnectionOpen() && time.Now().Before(deadline) {
		time.Sleep(100 * time.Millisecond)
	}

	if !client.paho.IsConnectionOpen() {
		t.Error("expected client to reconnect after broker restart")
	}

	// Close the client BEFORE the broker to avoid hanging on token wait.
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if err := client.Close(ctx); err != nil {
		t.Errorf("Close() error: %v", err)
	}

	_ = srv2.Close()
}

func TestClose_Clean(t *testing.T) {
	srv := startBroker(t)
	addr := brokerAddr(srv)

	client, err := New(testConfig(addr), "myhost", "myuser", testLogger())
	if err != nil {
		t.Fatalf("New() error: %v", err)
	}

	received := make(chan string, 2)
	subOpts := paho.NewClientOptions().
		AddBroker(addr).
		SetClientID("test-sub-close").
		SetAutoReconnect(false)
	subClient := paho.NewClient(subOpts)
	tok := subClient.Connect()
	if !tok.WaitTimeout(5 * time.Second) {
		t.Fatal("subscriber connect timeout")
	}
	defer subClient.Disconnect(100)

	onlineTopic := client.Topic("online")
	tok = subClient.Subscribe(onlineTopic, 1, func(_ paho.Client, msg paho.Message) {
		received <- string(msg.Payload())
	})
	if !tok.WaitTimeout(5 * time.Second) {
		t.Fatal("subscribe timeout")
	}

	// Drain initial retained "online".
	select {
	case <-received:
	case <-time.After(2 * time.Second):
	}

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if err := client.Close(ctx); err != nil {
		t.Fatalf("Close() error: %v", err)
	}

	select {
	case payload := <-received:
		if payload != "offline" {
			t.Errorf("expected 'offline' on close, got %q", payload)
		}
	case <-time.After(3 * time.Second):
		t.Error("timed out waiting for 'offline' message on close")
	}

	if client.paho.IsConnected() {
		t.Error("expected disconnected after Close()")
	}

	// Double-close must be safe.
	if err := client.Close(context.Background()); err != nil {
		t.Errorf("double Close() error: %v", err)
	}
}

func TestClose_NoGoroutineLeak(t *testing.T) {
	srv := startBroker(t)
	addr := brokerAddr(srv)

	client, err := New(testConfig(addr), "myhost", "myuser", testLogger())
	if err != nil {
		t.Fatalf("New() error: %v", err)
	}

	for i := 0; i < 5; i++ {
		topic := client.Topic("test", "msg")
		if err := client.Publish(topic, 1, false, []byte("payload")); err != nil {
			t.Fatalf("Publish() error: %v", err)
		}
	}

	time.Sleep(200 * time.Millisecond)

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if err := client.Close(ctx); err != nil {
		t.Fatalf("Close() error: %v", err)
	}

	time.Sleep(200 * time.Millisecond)

	if client.paho.IsConnected() {
		t.Error("expected disconnected")
	}
	if ec := client.ErrorCount(); ec != 0 {
		t.Errorf("expected 0 errors, got %d", ec)
	}
}

func TestPublish_ErrorCounting(t *testing.T) {
	srv := startBroker(t)
	addr := brokerAddr(srv)

	client, err := New(testConfig(addr), "myhost", "myuser", testLogger())
	if err != nil {
		t.Fatalf("New() error: %v", err)
	}
	t.Cleanup(func() { _ = client.Close(context.Background()) })

	ch := make(chan int)
	err = client.Publish(client.Topic("test"), 1, false, ch)
	if err == nil {
		t.Error("expected error for un-marshalable payload")
	}
	if client.ErrorCount() != 1 {
		t.Errorf("expected error count 1, got %d", client.ErrorCount())
	}
}

func TestPublishJSON_MarshalError(t *testing.T) {
	srv := startBroker(t)
	addr := brokerAddr(srv)

	client, err := New(testConfig(addr), "myhost", "myuser", testLogger())
	if err != nil {
		t.Fatalf("New() error: %v", err)
	}
	t.Cleanup(func() { _ = client.Close(context.Background()) })

	ch := make(chan int)
	err = client.PublishJSON(client.Topic("test"), ch)
	if err == nil {
		t.Error("expected error for un-marshalable JSON payload")
	}
	if client.ErrorCount() != 1 {
		t.Errorf("expected error count 1, got %d", client.ErrorCount())
	}
}

func TestPublish_StringPayload(t *testing.T) {
	srv := startBroker(t)
	addr := brokerAddr(srv)

	client, err := New(testConfig(addr), "myhost", "myuser", testLogger())
	if err != nil {
		t.Fatalf("New() error: %v", err)
	}
	t.Cleanup(func() { _ = client.Close(context.Background()) })

	received := make(chan string, 1)
	subOpts := paho.NewClientOptions().
		AddBroker(addr).
		SetClientID("test-sub-string").
		SetAutoReconnect(false)
	subClient := paho.NewClient(subOpts)
	tok := subClient.Connect()
	if !tok.WaitTimeout(5 * time.Second) {
		t.Fatal("subscriber connect timeout")
	}
	defer subClient.Disconnect(100)

	topic := client.Topic("test", "string")
	tok = subClient.Subscribe(topic, 1, func(_ paho.Client, msg paho.Message) {
		received <- string(msg.Payload())
	})
	if !tok.WaitTimeout(5 * time.Second) {
		t.Fatal("subscribe timeout")
	}

	time.Sleep(100 * time.Millisecond)

	if err := client.Publish(topic, 1, false, "hello mqtt"); err != nil {
		t.Fatalf("Publish() error: %v", err)
	}

	select {
	case payload := <-received:
		if payload != "hello mqtt" {
			t.Errorf("got %q, want %q", payload, "hello mqtt")
		}
	case <-time.After(5 * time.Second):
		t.Fatal("timed out waiting for string message")
	}
}

func TestConcurrentPublish(t *testing.T) {
	srv := startBroker(t)
	addr := brokerAddr(srv)

	client, err := New(testConfig(addr), "myhost", "myuser", testLogger())
	if err != nil {
		t.Fatalf("New() error: %v", err)
	}
	t.Cleanup(func() { _ = client.Close(context.Background()) })

	var wg sync.WaitGroup
	for i := 0; i < 20; i++ {
		wg.Add(1)
		go func(n int) {
			defer wg.Done()
			topic := client.Topic("test", "concurrent")
			_ = client.PublishJSON(topic, map[string]int{"n": n})
		}(i)
	}
	wg.Wait()

	if ec := client.ErrorCount(); ec != 0 {
		t.Errorf("expected 0 errors, got %d", ec)
	}
}
