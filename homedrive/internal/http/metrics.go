package http

import (
	"fmt"
	"io"
	"sort"
	"sync"
	"sync/atomic"
)

// Metrics collects counters and gauges exposed via GET /metrics in
// Prometheus text exposition format.
type Metrics struct {
	counters sync.Map // map[string]*int64
	gauges   sync.Map // map[string]*int64
}

// NewMetrics returns a ready-to-use Metrics instance.
func NewMetrics() *Metrics {
	return &Metrics{}
}

// IncCounter increments the named counter by 1.
func (m *Metrics) IncCounter(name string) {
	m.AddCounter(name, 1)
}

// AddCounter increments the named counter by delta.
func (m *Metrics) AddCounter(name string, delta int64) {
	v, _ := m.counters.LoadOrStore(name, new(int64))
	atomic.AddInt64(v.(*int64), delta)
}

// SetGauge sets the named gauge to the given value.
func (m *Metrics) SetGauge(name string, value int64) {
	v, _ := m.gauges.LoadOrStore(name, new(int64))
	atomic.StoreInt64(v.(*int64), value)
}

// WriteTo writes all metrics in Prometheus text exposition format.
func (m *Metrics) WriteTo(w io.Writer) (int64, error) {
	var total int64

	entries := m.collect("counter", &m.counters)
	entries = append(entries, m.collect("gauge", &m.gauges)...)
	sort.Slice(entries, func(i, j int) bool {
		return entries[i].name < entries[j].name
	})

	for _, e := range entries {
		n, err := fmt.Fprintf(w, "# TYPE %s %s\n%s %d\n", e.name, e.typ, e.name, e.value)
		total += int64(n)
		if err != nil {
			return total, fmt.Errorf("writing metric %s: %w", e.name, err)
		}
	}
	return total, nil
}

type metricEntry struct {
	name  string
	typ   string
	value int64
}

func (m *Metrics) collect(typ string, store *sync.Map) []metricEntry {
	var entries []metricEntry
	store.Range(func(key, val any) bool {
		entries = append(entries, metricEntry{
			name:  key.(string),
			typ:   typ,
			value: atomic.LoadInt64(val.(*int64)),
		})
		return true
	})
	return entries
}
