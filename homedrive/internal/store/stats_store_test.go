package store

import "testing"

func TestJournalStats_RoundTrip(t *testing.T) {
	tests := []struct {
		name         string
		puts         map[string][]byte
		deleteBefore string
		meta         map[string]string
		wantKeys     []string
	}{
		{"puts_only", map[string][]byte{"202611150000": []byte(`{}`), "202611150005": []byte(`{}`)}, "", map[string]string{"stats.last.push": "x"}, []string{"202611150000", "202611150005"}},
		{"prune_older", map[string][]byte{"202611010000": []byte(`{}`), "202611150005": []byte(`{}`)}, "202611070000", nil, []string{"202611150005"}},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			j := newTestJournal(t)
			if err := j.SaveStats(tc.puts, tc.deleteBefore, tc.meta); err != nil {
				t.Fatalf("SaveStats: %v", err)
			}
			if err := j.SetMeta([]byte("unrelated"), "v"); err != nil {
				t.Fatal(err)
			}
			buckets, meta, err := j.LoadStats()
			if err != nil {
				t.Fatalf("LoadStats: %v", err)
			}
			if len(buckets) != len(tc.wantKeys) {
				t.Fatalf("buckets = %d, want %d", len(buckets), len(tc.wantKeys))
			}
			for _, k := range tc.wantKeys {
				if _, ok := buckets[k]; !ok {
					t.Errorf("missing bucket %s", k)
				}
			}
			if len(meta) != len(tc.meta) {
				t.Errorf("meta = %v, want %v", meta, tc.meta)
			}
			for k, v := range tc.meta {
				if meta[k] != v {
					t.Errorf("meta[%s] = %q, want %q", k, meta[k], v)
				}
			}
		})
	}
}
