package log

import (
	"context"
	"sync"

	"go.opentelemetry.io/otel/attribute"
	otelmetric "go.opentelemetry.io/otel/metric"
	"go.opentelemetry.io/otel/sdk/metric"

	"github.com/tetratelabs/telemetry"
)

// NewOTelSink returns a telemetry.MetricSink backed by an OTel MeterProvider.
// Pass the same MeterProvider you use for Prometheus export — metrics created
// here flow through the same pipeline.
func NewOTelSink(mp *metric.MeterProvider, name string) *OTelSink {
	return &OTelSink{meter: mp.Meter(name)}
}

// OTelSink implements telemetry.MetricSink via the OTel metrics SDK.
type OTelSink struct {
	meter otelmetric.Meter
}

func (s *OTelSink) NewSum(name, desc string, opts ...telemetry.MetricOption) telemetry.Metric {
	o := applyOpts(opts)
	c, _ := s.meter.Int64Counter(name,
		otelmetric.WithDescription(desc),
		otelmetric.WithUnit(string(o.Unit)),
	)
	return &otelCounter{c: c, labels: o.Labels}
}

func (s *OTelSink) NewGauge(name, desc string, opts ...telemetry.MetricOption) telemetry.Metric {
	o := applyOpts(opts)
	g, _ := s.meter.Int64UpDownCounter(name,
		otelmetric.WithDescription(desc),
		otelmetric.WithUnit(string(o.Unit)),
	)
	return &otelGauge{g: g, labels: o.Labels}
}

func (s *OTelSink) NewDistribution(name, desc string, bounds []float64, opts ...telemetry.MetricOption) telemetry.Metric {
	o := applyOpts(opts)
	h, _ := s.meter.Float64Histogram(name,
		otelmetric.WithDescription(desc),
		otelmetric.WithUnit(string(o.Unit)),
		otelmetric.WithExplicitBucketBoundaries(bounds...),
	)
	return &otelHistogram{h: h, labels: o.Labels}
}

func (s *OTelSink) NewLabel(name string) telemetry.Label {
	return &otelLabel{key: attribute.Key(name)}
}

func (s *OTelSink) ContextWithLabels(ctx context.Context, vals ...telemetry.LabelValue) (context.Context, error) {
	var kvs []any
	for _, v := range vals {
		if lv, ok := v.(*otelLabelValue); ok {
			kvs = append(kvs, lv.kv.Key, lv.kv.Value.AsString())
		}
	}
	return telemetry.KeyValuesToContext(ctx, kvs...), nil
}

// --- otelCounter (Sum) ---

type otelCounter struct {
	c      otelmetric.Int64Counter
	labels []telemetry.Label
	with   []attribute.KeyValue
}

func (m *otelCounter) Increment()                                     { m.Record(1) }
func (m *otelCounter) Decrement()                                     { m.Record(-1) }
func (m *otelCounter) Name() string                                   { return "" }
func (m *otelCounter) Record(v float64)                               { m.RecordContext(context.Background(), v) }
func (m *otelCounter) RecordContext(ctx context.Context, v float64) {
	if ctx == nil {
		ctx = context.Background()
	}
	m.c.Add(ctx, int64(v), otelmetric.WithAttributes(m.attrs(ctx)...))
}
func (m *otelCounter) With(vals ...telemetry.LabelValue) telemetry.Metric {
	c := *m
	c.with = append(append([]attribute.KeyValue(nil), m.with...), toAttrs(vals)...)
	return &c
}

func (m *otelCounter) attrs(ctx context.Context) []attribute.KeyValue {
	// merge pre-set With() attrs + context-carried label KVPs
	attrs := append([]attribute.KeyValue(nil), m.with...)
	for _, kv := range contextAttrs(ctx) {
		attrs = append(attrs, kv)
	}
	return attrs
}

// --- otelGauge (UpDownCounter) ---

type otelGauge struct {
	g      otelmetric.Int64UpDownCounter
	labels []telemetry.Label
	with   []attribute.KeyValue
}

func (m *otelGauge) Increment()                                     { m.Record(1) }
func (m *otelGauge) Decrement()                                     { m.Record(-1) }
func (m *otelGauge) Name() string                                   { return "" }
func (m *otelGauge) Record(v float64)                               { m.RecordContext(context.Background(), v) }
func (m *otelGauge) RecordContext(ctx context.Context, v float64)   {
	m.g.Add(ctx, int64(v), otelmetric.WithAttributes(m.with...))
}
func (m *otelGauge) With(vals ...telemetry.LabelValue) telemetry.Metric {
	c := *m
	c.with = append(append([]attribute.KeyValue(nil), m.with...), toAttrs(vals)...)
	return &c
}

// --- otelHistogram (Distribution) ---

type otelHistogram struct {
	h      otelmetric.Float64Histogram
	labels []telemetry.Label
	with   []attribute.KeyValue
}

func (m *otelHistogram) Increment()                                     { m.Record(1) }
func (m *otelHistogram) Decrement()                                     { m.Record(-1) }
func (m *otelHistogram) Name() string                                   { return "" }
func (m *otelHistogram) Record(v float64)                               { m.RecordContext(context.Background(), v) }
func (m *otelHistogram) RecordContext(ctx context.Context, v float64)   {
	m.h.Record(ctx, v, otelmetric.WithAttributes(m.with...))
}
func (m *otelHistogram) With(vals ...telemetry.LabelValue) telemetry.Metric {
	c := *m
	c.with = append(append([]attribute.KeyValue(nil), m.with...), toAttrs(vals)...)
	return &c
}

// --- otelLabel ---

type otelLabel struct{ key attribute.Key }

func (l *otelLabel) Insert(v string) telemetry.LabelValue { return &otelLabelValue{l.key.String(v)} }
func (l *otelLabel) Update(v string) telemetry.LabelValue { return &otelLabelValue{l.key.String(v)} }
func (l *otelLabel) Upsert(v string) telemetry.LabelValue { return &otelLabelValue{l.key.String(v)} }
func (l *otelLabel) Delete() telemetry.LabelValue         { return &otelLabelValue{l.key.String("")} }

type otelLabelValue struct{ kv attribute.KeyValue }

// --- helpers ---

func applyOpts(opts []telemetry.MetricOption) telemetry.MetricOptions {
	o := telemetry.MetricOptions{}
	for _, opt := range opts {
		opt(&o)
	}
	return o
}

func toAttrs(vals []telemetry.LabelValue) []attribute.KeyValue {
	attrs := make([]attribute.KeyValue, 0, len(vals))
	for _, v := range vals {
		if lv, ok := v.(*otelLabelValue); ok {
			attrs = append(attrs, lv.kv)
		}
	}
	return attrs
}

// contextAttrs reads key-value pairs stored by KeyValuesToContext and
// converts them to OTel attributes for metric recording.
var contextAttrsPool = sync.Pool{New: func() any { return make([]attribute.KeyValue, 0, 8) }}

func contextAttrs(ctx context.Context) []attribute.KeyValue {
	if ctx == nil {
		return nil
	}
	kvs := telemetry.KeyValuesFromContext(ctx)
	if len(kvs) == 0 {
		return nil
	}
	attrs := make([]attribute.KeyValue, 0, len(kvs)/2)
	for i := 0; i+1 < len(kvs); i += 2 {
		k, _ := kvs[i].(string)
		v, _ := kvs[i+1].(string)
		if k != "" {
			attrs = append(attrs, attribute.String(k, v))
		}
	}
	return attrs
}
