package stats

import (
	"time"

	"github.com/asnowfix/home-drive/homedrive/pkg/homedrive"
)

const (
	// BucketSize is the width of one counter bucket. Windows are aligned
	// to it, so 1h/24h/7d sums are accurate to this granularity.
	BucketSize = 5 * time.Minute

	// Retention is how long buckets are kept (7d window plus a day of
	// slack). Older buckets are pruned from memory and from Bolt.
	Retention = 8 * 24 * time.Hour

	// keyLayout formats a bucket start as its Bolt key (UTC).
	keyLayout = "200601021504"
)

// windows are the reported windows, in the order of the status document.
var windows = []struct {
	name string
	dur  time.Duration
}{
	{homedrive.Window1h, time.Hour},
	{homedrive.Window24h, 24 * time.Hour},
	{homedrive.Window7d, 7 * 24 * time.Hour},
}

// bucket holds the counters of one BucketSize interval. Short omitempty
// JSON keys keep the persisted value compact.
type bucket struct {
	FilesUp   int64 `json:"fu,omitempty"`
	FilesDown int64 `json:"fd,omitempty"`
	BytesUp   int64 `json:"bu,omitempty"`
	BytesDown int64 `json:"bd,omitempty"`
	Errors    int64 `json:"er,omitempty"`
	Conflicts int64 `json:"cf,omitempty"`
	Starts    int64 `json:"st,omitempty"`
}

// addTo accumulates b into c.
func (b *bucket) addTo(c *homedrive.Counts) {
	c.FilesUp += b.FilesUp
	c.FilesDown += b.FilesDown
	c.BytesUp += b.BytesUp
	c.BytesDown += b.BytesDown
	c.Errors += b.Errors
	c.Conflicts += b.Conflicts
	c.Starts += b.Starts
}

// bucketStart returns the start of the bucket containing t (UTC).
func bucketStart(t time.Time) time.Time {
	return t.UTC().Truncate(BucketSize)
}

// bucketKey is the Bolt key of the bucket starting at start.
func bucketKey(start time.Time) string {
	return start.UTC().Format(keyLayout)
}

// sumWindows sums the buckets whose start lies in [now-W, now] for each
// window W. Future-dated buckets (clock skew) are ignored.
func sumWindows(buckets map[time.Time]*bucket, now time.Time) map[string]homedrive.Counts {
	out := make(map[string]homedrive.Counts, len(windows))
	for _, w := range windows {
		var c homedrive.Counts
		lo := now.Add(-w.dur)
		for start, b := range buckets {
			if start.Before(lo) || start.After(now) {
				continue
			}
			b.addTo(&c)
		}
		out[w.name] = c
	}
	return out
}
