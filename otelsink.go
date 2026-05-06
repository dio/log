package log

import (
	"context"
	"slices"

	"go.opentelemetry.io/otel/attribute"
	otelmetric "go.opentelemetry.io/otel/metric"
	"go.opentelemetry.io/otel/sdk/metric"

	"github.com/tetratelabs/telemetry"
)

// NewOTelSink returns a telemetry.MetricSink backed by an OTel MeterProvider.
// Pass the same MeterProvider you use for Prometheus export; metrics created
// here flow through the same pipeline.
//
// Call [OTelSink.Shutdown] on service exit to flush pending metrics.
func NewOTelSink(mp *metric.MeterProvider, name string) *OTelSink {
	return &OTelSink{meter: mp.Meter(name), mp: mp}
}

// OTelSink implements telemetry.MetricSink via the OTel metrics SDK.
type OTelSink struct {
	meter otelmetric.Meter
	mp    *metric.MeterProvider
}

// Shutdown flushes and shuts down the underlying MeterProvider.
// Call this on service exit, typically via defer or a run.Group actor.
func (s *OTelSink) Shutdown(ctx context.Context) error {
	return s.mp.Shutdown(ctx)
}

func (s *OTelSink) NewSum(name, desc string, opts ...telemetry.MetricOption) telemetry.Metric {
	o := applyOpts(opts)
	c, _ := s.meter.Int64Counter(name,
		otelmetric.WithDescription(desc),
		otelmetric.WithUnit(string(o.Unit)),
	)
	return &otelCounter{base: otelBase{with: toAttrs(o.Labels)}, c: c}
}

func (s *OTelSink) NewGauge(name, desc string, opts ...telemetry.MetricOption) telemetry.Metric {
	o := applyOpts(opts)
	g, _ := s.meter.Int64UpDownCounter(name,
		otelmetric.WithDescription(desc),
		otelmetric.WithUnit(string(o.Unit)),
	)
	return &otelGauge{base: otelBase{with: toAttrs(o.Labels)}, g: g}
}

func (s *OTelSink) NewDistribution(name, desc string, bounds []float64, opts ...telemetry.MetricOption) telemetry.Metric {
	o := applyOpts(opts)
	h, _ := s.meter.Float64Histogram(name,
		otelmetric.WithDescription(desc),
		otelmetric.WithUnit(string(o.Unit)),
		otelmetric.WithExplicitBucketBoundaries(bounds...),
	)
	return &otelHistogram{base: otelBase{with: toAttrs(o.Labels)}, h: h}
}

func (s *OTelSink) NewLabel(name string) telemetry.Label {
	return &otelLabel{key: attribute.Key(name)}
}

func (s *OTelSink) ContextWithLabels(ctx context.Context, vals ...telemetry.LabelValue) (context.Context, error) {
	kvs := make([]any, 0, len(vals)*2)
	for _, v := range vals {
		if lv, ok := v.(*otelLabelValue); ok {
			kvs = append(kvs, lv.kv.Key, lv.kv.Value.AsString())
		}
	}
	return telemetry.KeyValuesToContext(ctx, kvs...), nil
}

// otelBase holds the pre-set attribute slice shared by all metric types.
// With() returns a copy with additional attributes appended.
type otelBase struct {
	with []attribute.KeyValue
}

func (b otelBase) withAttrs(vals []telemetry.LabelValue) otelBase {
	return otelBase{with: append(slices.Clone(b.with), toAttrs(vals)...)}
}

func (b otelBase) attrs(ctx context.Context) []attribute.KeyValue {
	return append(slices.Clone(b.with), contextAttrs(ctx)...)
}

// --- otelCounter (Sum) ---

type otelCounter struct {
	base otelBase
	c    otelmetric.Int64Counter
}

func (m *otelCounter) Increment()   { m.Record(1) }
func (m *otelCounter) Decrement()   { m.Record(-1) }
func (m *otelCounter) Name() string { return "" }
func (m *otelCounter) Record(v float64) {
	m.RecordContext(context.Background(), v)
}
func (m *otelCounter) RecordContext(ctx context.Context, v float64) {
	if ctx == nil {
		ctx = context.Background()
	}
	m.c.Add(ctx, int64(v), otelmetric.WithAttributes(m.base.attrs(ctx)...))
}
func (m *otelCounter) With(vals ...telemetry.LabelValue) telemetry.Metric {
	return &otelCounter{base: m.base.withAttrs(vals), c: m.c}
}

// --- otelGauge (UpDownCounter) ---

type otelGauge struct {
	base otelBase
	g    otelmetric.Int64UpDownCounter
}

func (m *otelGauge) Increment()   { m.Record(1) }
func (m *otelGauge) Decrement()   { m.Record(-1) }
func (m *otelGauge) Name() string { return "" }
func (m *otelGauge) Record(v float64) {
	m.RecordContext(context.Background(), v)
}
func (m *otelGauge) RecordContext(ctx context.Context, v float64) {
	m.g.Add(ctx, int64(v), otelmetric.WithAttributes(m.base.with...))
}
func (m *otelGauge) With(vals ...telemetry.LabelValue) telemetry.Metric {
	return &otelGauge{base: m.base.withAttrs(vals), g: m.g}
}

// --- otelHistogram (Distribution) ---

type otelHistogram struct {
	base otelBase
	h    otelmetric.Float64Histogram
}

func (m *otelHistogram) Increment()   { m.Record(1) }
func (m *otelHistogram) Decrement()   { m.Record(-1) }
func (m *otelHistogram) Name() string { return "" }
func (m *otelHistogram) Record(v float64) {
	m.RecordContext(context.Background(), v)
}
func (m *otelHistogram) RecordContext(ctx context.Context, v float64) {
	m.h.Record(ctx, v, otelmetric.WithAttributes(m.base.with...))
}
func (m *otelHistogram) With(vals ...telemetry.LabelValue) telemetry.Metric {
	return &otelHistogram{base: m.base.withAttrs(vals), h: m.h}
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

// toAttrs converts telemetry.Label / LabelValue slices to OTel attributes.
// Accepts []telemetry.Label (from MetricOptions) by pulling the otelLabel key.
func toAttrs(vals any) []attribute.KeyValue {
	switch v := vals.(type) {
	case []telemetry.LabelValue:
		attrs := make([]attribute.KeyValue, 0, len(v))
		for _, lv := range v {
			if olv, ok := lv.(*otelLabelValue); ok {
				attrs = append(attrs, olv.kv)
			}
		}
		return attrs
	case []telemetry.Label:
		return nil // labels are dimensions; values come via With()
	}
	return nil
}

// contextAttrs reads key-value pairs stored by KeyValuesToContext and
// converts them to OTel attributes for metric recording.
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
