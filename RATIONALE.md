# Rationale

Why this library exists and the specific decisions it makes.

---

## The insight: most logs should be metrics

A single log line that says `"request failed"` is rarely actionable on its own.
One error in isolation could be a transient blip. What's actually actionable is
when the **frequency** of that message changes. Ten errors per second after days
of zero is your alert condition.

This means most Info and Error log lines in a service should have a paired metric.
The metric is what drives alerting. The log is what drives debugging after the alert
fires. They serve different purposes but belong to the same event.

> This framing comes from [tetratelabs/telemetry](https://github.com/tetratelabs/telemetry),
> which states it plainly in its doc comment: *"Most logs should be metrics."*

---

## The problem with two separate calls

The conventional approach is two separate calls:

```go
log.Info("request handled", "route", route)
requestCounter.Add(ctx, 1, metric.WithAttributes(attribute.String("route", route)))
```

This breaks in two ways:

**1. You forget one.** Under pressure, when adding a new code path or fixing a bug,
you add the log and forget the metric. The alert never fires. You only find out during
an incident.

**2. Silencing logs silences metrics.** When you tune log verbosity in production,
setting level to Warn or Error to cut noise, the Info log disappears. If the metric
was only emitted from the same `if level >= Info` branch, the metric disappears too.
Your dashboard goes dark exactly when you most want signal.

---

## The fix: unconditional metric emission

The core mechanic in this library is that `RecordContext` is called **before** the
level check:

```go
func (l *slogLogger) Info(msg string, kvs ...any) {
    if l.metric != nil {
        l.metric.RecordContext(l.ctx, 1) // ← always, regardless of level
    }
    if l.level != telemetry.LevelNone && l.level < telemetry.LevelInfo {
        return // log silenced, metric already recorded
    }
    l.sl.Info(msg, l.args(kvs)...)
}
```

This is not an accident. It is the single most important invariant in the library.
Changing this ordering would silently break every alert that relies on it.

The `LevelNone` carve-out is for the `scope` package's default level. When a scope
has not been explicitly configured, `LevelNone` means "inherit the underlying logger's
level" rather than "silence everything."

---

## Why slog as the backend

`log/slog` is stdlib since Go 1.21. It has structured key-value logging, level
filtering, and a pluggable `Handler` interface. It is the right default for new Go
services that don't want to pull in zap or zerolog.

The `tetratelabs/telemetry` library is backend-agnostic. It defines a `Logger`
interface and a `function` package with a concrete adapter. We implement that
interface directly over slog to keep the dep graph flat: one import for the
telemetry abstraction, stdlib for the logging backend.

---

## Why OTel for metrics

The `OTelSink` backed by an OTel `MeterProvider` means:

- The same metric flows to Prometheus (via the Prometheus exporter) **and** to an
  OTLP collector (Grafana, Jaeger, Honeycomb, etc.) simultaneously. Just configure
  multiple readers on the provider.
- Metrics carry the same resource attributes (`service.name`, `service.version`,
  host info) as traces, so correlation in a backend like Grafana is automatic.
- No custom Prometheus registry, no manual `prometheus.MustRegister`. The OTel SDK
  handles registration, cardinality limits, and exemplars.

For tests, `MemSink` skips all of that and stores values in a plain `map[string]float64`.
Fast, zero-dep, easy to assert.

---

## Why context carries trace_id into logs

When an OTel span is active in the context attached via `.Context(ctx)`, the logger
automatically injects `trace_id` and `span_id` into the log line:

```
level=INFO msg="request handled" trace_id=c02b2a3a... span_id=d1449529... route=/api/v1/users
```

This links the log to its span without requiring a log aggregation pipeline to do
the join. Loki, Datadog, and Cloud Logging all support `trace_id` as a correlation
field natively. The join is free.

The alternative is to manually extract `span.SpanContext().TraceID().String()` and
pass it as a key-value pair in every log call. This is exactly the kind of boilerplate
that gets forgotten under pressure.

---

## Why the e2e uses an in-process sink instead of a real collector

The e2e test needs to assert that specific metric values, log bodies, and span names
arrive at a collector. Two options exist:

**Real collector (otel-front, Jaeger, etc.):** Requires Docker, has startup latency,
and the REST API only exposes coarse queries (does metric name exist? does trace ID
return 200?). No way to assert `app_requests_total{route="/api/v1/users"} == 1`
without parsing the Prometheus text format.

**In-process OTLP gRPC sink:** Starts on a random port in `TestMain`, receives the
same proto messages a real collector would, stores them in memory, exposes `WaitFor*`
helpers that poll at 100ms. The whole suite runs in under one second. No Docker. No
flakiness from container startup timing. Exact assertions on values, labels, and
attributes.

The in-process sink is the right default for CI. The real collector is the right tool
for *human* verification. See the correlated trace/log/metric in a UI. Both are
supported: `go test -tags e2e` for CI, `E2E_OTEL_FRONT=1 go test -tags e2e` for
visual inspection.
