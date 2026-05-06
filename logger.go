// Package log provides a slog-backed implementation of tetratelabs/telemetry.Logger
// optimized for OpenTelemetry: metrics flow through an OTel MeterProvider, and
// every log line automatically carries the OTel trace_id and span_id from context
// when a span is active, making logs and traces trivially correlatable.
//
// # Quick start
//
//	// Wire once in main:
//	sink := log.NewOTelSink(meterProvider, "zia")
//	telemetry.SetGlobalMetricSink(sink)
//	scope.UseLogger(log.New(slog.Default()))
//
//	// In library code:
//	logger.Metric(errMetric).Error("reserve failed", err, "cluster", cluster)
//	// → slog:       level=ERROR msg="reserve failed" trace_id=abc span_id=def cluster=openai err=...
//	// → OTel metric: zia_reserve_errors_total{cluster="openai"} += 1
package log

import (
	"context"
	"log/slog"

	"go.opentelemetry.io/otel/trace"

	"github.com/tetratelabs/telemetry"
)

// New returns a telemetry.Logger backed by the given *slog.Logger.
// When a context with an active OTel span is attached via .Context(ctx),
// trace_id and span_id are automatically added to every log line.
func New(sl *slog.Logger) telemetry.Logger {
	return &slogLogger{sl: sl, level: telemetry.LevelInfo, ctx: context.Background()}
}

type slogLogger struct {
	sl     *slog.Logger
	kvs    []any
	ctx    context.Context
	metric telemetry.Metric
	level  telemetry.Level
}

func (l *slogLogger) Debug(msg string, kvs ...any) {
	if l.level != telemetry.LevelNone && l.level < telemetry.LevelDebug {
		return
	}
	l.sl.Debug(msg, l.args(kvs)...)
}

func (l *slogLogger) Info(msg string, kvs ...any) {
	if l.metric != nil {
		l.metric.RecordContext(l.ctx, 1) // fires before level check, unconditional
	}
	if l.level != telemetry.LevelNone && l.level < telemetry.LevelInfo {
		return
	}
	l.sl.Info(msg, l.args(kvs)...)
}

func (l *slogLogger) Error(msg string, err error, kvs ...any) {
	if l.metric != nil {
		l.metric.RecordContext(l.ctx, 1)
	}
	// Error always emits (LevelError=1 is the minimum non-zero level).
	args := l.args(kvs)
	if err != nil {
		args = append(args, "err", err)
	}
	l.sl.Error(msg, args...)
}

func (l *slogLogger) SetLevel(lvl telemetry.Level) { l.level = lvl }
func (l *slogLogger) Level() telemetry.Level        { return l.level }

func (l *slogLogger) With(kvs ...any) telemetry.Logger {
	c := l.clone()
	c.kvs = append(c.kvs, kvs...)
	return c
}

func (l *slogLogger) Context(ctx context.Context) telemetry.Logger {
	c := l.clone()
	c.ctx = ctx

	// Pull KVPs stored in context by KeyValuesToContext.
	if fromCtx := telemetry.KeyValuesFromContext(ctx); len(fromCtx) > 0 {
		c.kvs = append(c.kvs, fromCtx...)
	}

	// Inject OTel trace_id + span_id if a span is active.
	if sc := trace.SpanFromContext(ctx).SpanContext(); sc.IsValid() {
		c.kvs = append(c.kvs,
			"trace_id", sc.TraceID().String(),
			"span_id", sc.SpanID().String(),
		)
	}

	return c
}

func (l *slogLogger) Metric(m telemetry.Metric) telemetry.Logger {
	c := l.clone()
	c.metric = m
	return c
}

func (l *slogLogger) Clone() telemetry.Logger { return l.clone() }

// args merges persistent kvs with per-call kvs.
func (l *slogLogger) args(kvs []any) []any {
	if len(l.kvs) == 0 {
		return kvs
	}
	merged := make([]any, 0, len(l.kvs)+len(kvs))
	merged = append(merged, l.kvs...)
	merged = append(merged, kvs...)
	return merged
}

func (l *slogLogger) clone() *slogLogger {
	c := *l
	c.kvs = append([]any(nil), l.kvs...)
	return &c
}
