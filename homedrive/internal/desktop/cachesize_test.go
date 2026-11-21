package desktop

import (
	"testing"
	"time"
)

func TestEffectiveMaxSize_Cases(t *testing.T) {
	tests := []struct {
		name   string
		cache  CacheCfg
		fsSize int64
		want   int64
	}{
		{"PercentOfFilesystem", CacheCfg{MaxSizePercent: 25}, 1000 << 30, 250 << 30},
		{"AbsoluteOverrides", CacheCfg{MaxSize: 10 << 30, MaxSizePercent: 25}, 1000 << 30, 10 << 30},
		{"OnePercent", CacheCfg{MaxSizePercent: 1}, 100, 1},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			if got := tc.cache.EffectiveMaxSize(tc.fsSize); got != tc.want {
				t.Errorf("got %d, want %d", got, tc.want)
			}
		})
	}
}

// TestEffectiveMaxAge_Cases guards the rclone v1.76.0 quirk: 0 must never
// reach rclone, where it means "evict everything idle at every poll".
func TestEffectiveMaxAge_Cases(t *testing.T) {
	tests := []struct {
		name string
		in   time.Duration
		want time.Duration
	}{
		{"ZeroMeansNoLimit", 0, noAgeLimit},
		{"NegativeMeansNoLimit", -time.Hour, noAgeLimit},
		{"ExplicitKept", 48 * time.Hour, 48 * time.Hour},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			c := CacheCfg{MaxAge: Duration{tc.in}}
			if got := c.EffectiveMaxAge(); got != tc.want {
				t.Errorf("got %v, want %v", got, tc.want)
			}
		})
	}
	if noAgeLimit <= 0 {
		t.Fatal("noAgeLimit overflowed")
	}
}
