package http

import (
	"bytes"
	"strings"
	"testing"
)

func TestMetrics_IncCounter(t *testing.T) {
	m := NewMetrics()
	m.IncCounter("test_counter")
	m.IncCounter("test_counter")
	m.IncCounter("test_counter")

	var buf bytes.Buffer
	if _, err := m.WriteTo(&buf); err != nil {
		t.Fatalf("WriteTo failed: %v", err)
	}

	got := buf.String()
	if !strings.Contains(got, "test_counter 3") {
		t.Errorf("expected counter=3 in:\n%s", got)
	}
	if !strings.Contains(got, "# TYPE test_counter counter") {
		t.Errorf("expected TYPE line in:\n%s", got)
	}
}

func TestMetrics_AddCounter(t *testing.T) {
	m := NewMetrics()
	m.AddCounter("bytes_total", 100)
	m.AddCounter("bytes_total", 200)

	var buf bytes.Buffer
	if _, err := m.WriteTo(&buf); err != nil {
		t.Fatalf("WriteTo failed: %v", err)
	}

	got := buf.String()
	if !strings.Contains(got, "bytes_total 300") {
		t.Errorf("expected counter=300 in:\n%s", got)
	}
}

func TestMetrics_SetGauge(t *testing.T) {
	m := NewMetrics()
	m.SetGauge("queue_size", 10)
	m.SetGauge("queue_size", 5)

	var buf bytes.Buffer
	if _, err := m.WriteTo(&buf); err != nil {
		t.Fatalf("WriteTo failed: %v", err)
	}

	got := buf.String()
	if !strings.Contains(got, "queue_size 5") {
		t.Errorf("expected gauge=5 (last set) in:\n%s", got)
	}
	if !strings.Contains(got, "# TYPE queue_size gauge") {
		t.Errorf("expected TYPE gauge in:\n%s", got)
	}
}

func TestMetrics_MultipleMetricsSorted(t *testing.T) {
	m := NewMetrics()
	m.IncCounter("z_counter")
	m.SetGauge("a_gauge", 1)
	m.IncCounter("m_counter")

	var buf bytes.Buffer
	if _, err := m.WriteTo(&buf); err != nil {
		t.Fatalf("WriteTo failed: %v", err)
	}

	got := buf.String()
	aIdx := strings.Index(got, "a_gauge")
	mIdx := strings.Index(got, "m_counter")
	zIdx := strings.Index(got, "z_counter")

	if aIdx >= mIdx || mIdx >= zIdx {
		t.Errorf("metrics should be sorted alphabetically, got:\n%s", got)
	}
}

func TestMetrics_EmptyWritesNothing(t *testing.T) {
	m := NewMetrics()

	var buf bytes.Buffer
	n, err := m.WriteTo(&buf)
	if err != nil {
		t.Fatalf("WriteTo failed: %v", err)
	}
	if n != 0 {
		t.Errorf("expected 0 bytes written, got %d", n)
	}
	if buf.Len() != 0 {
		t.Errorf("expected empty output, got: %s", buf.String())
	}
}
