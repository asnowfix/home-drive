package mqtt

import (
	"context"
	"encoding/json"
	"regexp"
	"strings"
	"testing"
	"time"

	"github.com/asnowfix/home-drive/homedrive/pkg/homedrive"
)

func testSnapshot() homedrive.StatusV2 {
	now := time.Date(2026, 11, 15, 12, 33, 13, 0, time.FixedZone("CET", 3600))
	pull := now.Add(-time.Minute)
	return homedrive.StatusV2{
		TS:        homedrive.Timestamp{Time: now},
		Version:   "test",
		State:     homedrive.StateRunning,
		StartedAt: homedrive.Timestamp{Time: now.Add(-time.Hour)},
		LastPull:  homedrive.TS(pull),
		Counts:    map[string]homedrive.Counts{homedrive.Window24h: {BytesDown: 42}},
	}
}

func TestStatePublisher_PublishOnceRetained(t *testing.T) {
	tests := []struct {
		name     string
		interval time.Duration
		wantSecs int
	}{
		{"configured", 45 * time.Second, 45},
		{"zero_defaults", 0, 30},
	}
	tsRe := regexp.MustCompile(`^\d{4}-\d\d-\d\dT\d\d:\d\d:\d\dZ$`)
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			addr, srv := startEmbeddedBroker(t)
			client, err := New(testConfig(addr), "h", "u", testLogger())
			if err != nil {
				t.Fatalf("New: %v", err)
			}
			t.Cleanup(func() { _ = client.Close(context.Background()) })

			sp := NewStatePublisher(client, testSnapshot, tc.interval, testLogger())
			sp.PublishOnce()
			time.Sleep(200 * time.Millisecond)

			// Subscribing after the publish only receives it if retained.
			ch := subscribeInlineWithTopic(t, srv, "homedrive/h/u/status")
			select {
			case msg := <-ch:
				var raw map[string]json.RawMessage
				if err := json.Unmarshal(msg.payload, &raw); err != nil {
					t.Fatalf("unmarshal: %v", err)
				}
				var schema string
				_ = json.Unmarshal(raw["schema"], &schema)
				if schema != "2.0" {
					t.Errorf("schema = %q, want 2.0", schema)
				}
				var secs int
				_ = json.Unmarshal(raw["publish_interval_s"], &secs)
				if secs != tc.wantSecs {
					t.Errorf("publish_interval_s = %d, want %d", secs, tc.wantSecs)
				}
				for _, k := range []string{"ts", "started_at", "last_pull"} {
					var s string
					_ = json.Unmarshal(raw[k], &s)
					if !tsRe.MatchString(s) {
						t.Errorf("%s = %q, want RFC3339 UTC Z", k, s)
					}
				}
				if string(raw["last_push"]) != "null" {
					t.Errorf("last_push = %s, want null", raw["last_push"])
				}
			case <-time.After(3 * time.Second):
				t.Fatal("no retained status received")
			}
		})
	}
}

func TestStatePublisher_OneDocumentPerTick(t *testing.T) {
	addr, srv := startEmbeddedBroker(t)
	client, err := New(testConfig(addr), "h", "u", testLogger())
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	t.Cleanup(func() { _ = client.Close(context.Background()) })

	ch := subscribeInlineWithTopic(t, srv, "homedrive/h/u/status")
	sp := NewStatePublisher(client, testSnapshot, time.Second, testLogger())
	for i := 0; i < 3; i++ {
		sp.PublishOnce()
		select {
		case msg := <-ch:
			if !strings.HasSuffix(msg.topic, "/status") {
				t.Errorf("topic = %q, want .../status", msg.topic)
			}
		case <-time.After(3 * time.Second):
			t.Fatalf("tick %d: no status", i)
		}
	}
}
