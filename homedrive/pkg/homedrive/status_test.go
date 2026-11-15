package homedrive

import (
	"encoding/json"
	"os"
	"path/filepath"
	"reflect"
	"regexp"
	"testing"
	"time"
)

var tsRe = regexp.MustCompile(`^\d{4}-\d\d-\d\dT\d\d:\d\d:\d\dZ$`)

func TestTimestamp_MarshalUTCZ(t *testing.T) {
	cet := time.FixedZone("CET", 3600)
	tests := []struct {
		name string
		in   time.Time
		want string
	}{
		{"utc", time.Date(2026, 11, 15, 12, 0, 0, 0, time.UTC), `"2026-11-15T12:00:00Z"`},
		{"offset_converted", time.Date(2026, 11, 15, 12, 0, 0, 0, cet), `"2026-11-15T11:00:00Z"`},
		{"subsecond_dropped", time.Date(2026, 11, 15, 12, 0, 0, 999, time.UTC), `"2026-11-15T12:00:00Z"`},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			b, err := json.Marshal(Timestamp{tc.in})
			if err != nil || string(b) != tc.want {
				t.Errorf("got %s (%v), want %s", b, err, tc.want)
			}
		})
	}
}

func TestStatusV2_NullableTimestamps(t *testing.T) {
	b, err := json.Marshal(StatusV2{Counts: map[string]Counts{}})
	if err != nil {
		t.Fatal(err)
	}
	var raw map[string]json.RawMessage
	if err := json.Unmarshal(b, &raw); err != nil {
		t.Fatal(err)
	}
	for _, k := range []string{"last_push", "last_pull", "last_activity", "last_poll", "last_error", "bisync", "local", "quota_used_pct"} {
		if string(raw[k]) != "null" {
			t.Errorf("%s = %s, want null", k, raw[k])
		}
	}
	if TS(time.Time{}) != nil {
		t.Error("TS(zero) should be nil")
	}
}

// TestStatusV2_ExampleFixtures pins the example documents shipped for the
// myhome consumer (status-v2.md §12): they must parse, carry schema 2.0 and
// a positive publish_interval_s, and use UTC "Z" second-precision stamps.
func TestStatusV2_ExampleFixtures(t *testing.T) {
	files, err := filepath.Glob("testdata/status-*.json")
	if err != nil || len(files) != 3 {
		t.Fatalf("fixtures = %v (%v), want 3", files, err)
	}
	for _, f := range files {
		t.Run(filepath.Base(f), func(t *testing.T) {
			data, err := os.ReadFile(f)
			if err != nil {
				t.Fatal(err)
			}
			var doc StatusV2
			if err := json.Unmarshal(data, &doc); err != nil {
				t.Fatalf("unmarshal: %v", err)
			}
			if doc.Schema != StatusSchema || doc.PublishIntervalS <= 0 {
				t.Errorf("schema %q publish_interval_s %d", doc.Schema, doc.PublishIntervalS)
			}
			for _, w := range []string{Window1h, Window24h, Window7d} {
				if _, ok := doc.Counts[w]; !ok {
					t.Errorf("missing counts[%s]", w)
				}
			}
			var raw map[string]any
			_ = json.Unmarshal(data, &raw)
			for _, k := range []string{"ts", "started_at", "last_push", "last_pull", "last_activity", "last_poll"} {
				if s, ok := raw[k].(string); ok && !tsRe.MatchString(s) {
					t.Errorf("%s = %q, want UTC Z", k, s)
				}
			}
			// Round trip: re-marshalling the parsed document must reproduce
			// the fixture key by key, so a renamed or retagged JSON field
			// fails here. "auth" is #142 and not yet part of StatusV2.
			out, err := json.Marshal(doc)
			if err != nil {
				t.Fatal(err)
			}
			var back map[string]any
			if err := json.Unmarshal(out, &back); err != nil {
				t.Fatal(err)
			}
			delete(raw, "auth")
			if !reflect.DeepEqual(back, raw) {
				t.Errorf("round trip differs:\n got  %v\n want %v", back, raw)
			}
		})
	}
}
