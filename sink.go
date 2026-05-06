package log

import (
	"context"
	"sync"

	"github.com/tetratelabs/telemetry"
)

// NewMemSink returns a MetricSink that stores observations in memory.
// Useful for development and tests; dump with sink.Snapshot().
func NewMemSink() *MemSink {
	return &MemSink{metrics: map[string]*memMetric{}}
}

// MemSink implements telemetry.MetricSink backed by in-memory counters.
type MemSink struct {
	mu      sync.Mutex
	metrics map[string]*memMetric
}

// Reset clears all recorded values. Zeroes values in place so existing
// Metric references (held in package-level vars) stay valid.
func (s *MemSink) Reset() {
	s.mu.Lock()
	defer s.mu.Unlock()
	for _, m := range s.metrics {
		m.mu.Lock()
		m.val = 0
		m.mu.Unlock()
	}
}

// Snapshot returns a copy of all recorded values keyed by metric name.
func (s *MemSink) Snapshot() map[string]float64 {
	s.mu.Lock()
	defer s.mu.Unlock()
	out := make(map[string]float64, len(s.metrics))
	for name, m := range s.metrics {
		out[name] = m.total()
	}
	return out
}

func (s *MemSink) get(name string) *memMetric {
	s.mu.Lock()
	defer s.mu.Unlock()
	m, ok := s.metrics[name]
	if !ok {
		m = &memMetric{name: name}
		s.metrics[name] = m
	}
	return m
}

func (s *MemSink) NewSum(name, _ string, _ ...telemetry.MetricOption) telemetry.Metric {
	return s.get(name)
}

func (s *MemSink) NewGauge(name, _ string, _ ...telemetry.MetricOption) telemetry.Metric {
	return s.get(name)
}

func (s *MemSink) NewDistribution(name, _ string, _ []float64, _ ...telemetry.MetricOption) telemetry.Metric {
	return s.get(name)
}

func (s *MemSink) NewLabel(name string) telemetry.Label {
	return &memLabel{key: name}
}

func (s *MemSink) ContextWithLabels(ctx context.Context, vals ...telemetry.LabelValue) (context.Context, error) {
	var kvs []any
	for _, v := range vals {
		if lv, ok := v.(*memLabelValue); ok {
			kvs = append(kvs, lv.key, lv.value)
		}
	}
	return telemetry.KeyValuesToContext(ctx, kvs...), nil
}

// --- memMetric ---

type memMetric struct {
	name string
	mu   sync.Mutex
	val  float64
}

func (m *memMetric) total() float64 {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.val
}

func (m *memMetric) add(v float64) {
	m.mu.Lock()
	m.val += v
	m.mu.Unlock()
}

func (m *memMetric) Increment()                                    { m.add(1) }
func (m *memMetric) Decrement()                                    { m.add(-1) }
func (m *memMetric) Record(v float64)                              { m.add(v) }
func (m *memMetric) RecordContext(_ context.Context, v float64)    { m.add(v) }
func (m *memMetric) Name() string                                  { return m.name }
func (m *memMetric) With(_ ...telemetry.LabelValue) telemetry.Metric { return m }

// --- memLabel ---

type memLabel struct{ key string }

func (l *memLabel) Insert(v string) telemetry.LabelValue { return &memLabelValue{l.key, v} }
func (l *memLabel) Update(v string) telemetry.LabelValue { return &memLabelValue{l.key, v} }
func (l *memLabel) Upsert(v string) telemetry.LabelValue { return &memLabelValue{l.key, v} }
func (l *memLabel) Delete() telemetry.LabelValue         { return &memLabelValue{l.key, ""} }

type memLabelValue struct {
	key   string
	value string
}
