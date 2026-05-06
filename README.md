# log

[![Go Reference](https://pkg.go.dev/badge/github.com/dio/log.svg)](https://pkg.go.dev/github.com/dio/log)
[![CI](https://github.com/dio/log/actions/workflows/ci.yml/badge.svg)](https://github.com/dio/log/actions/workflows/ci.yml)

A slog-backed [tetratelabs/telemetry](https://github.com/tetratelabs/telemetry) logger
optimized for OpenTelemetry, with one guarantee that matters in production:

> **When you silence logs, metrics still fire.**

---

## The problem it solves

In high-traffic services you suppress Info logs to cut noise and cost. The standard
pattern breaks when you do that:

```go
// Traditional — two separate calls
log.Info("reserve success")       // silenced at Error level → gone
reserveCounter.Add(ctx, 1)        // you have to remember to call this too

// What actually happens in production when level=Error:
log.Info("reserve success")       // ← silenced, nothing emitted
// reserveCounter.Add forgotten   // ← alert never fires
```

This library fixes it by making log and metric inseparable:

```go
// One call — log + metric always together
logger.Metric(reserveOK).Info("reserve success")
// level=Error: log silent, metric still fires
// level=Debug: both log and metric fire
```

The metric fires because `RecordContext` is called **before** the level check. See
[RATIONALE.md](RATIONALE.md) for the full reasoning.

---

## Install

```bash
go get github.com/dio/log
```

---

## Usage

### 1. Wire once in main

```go
import (
    "log/slog"

    ziolog "github.com/dio/log"
    "github.com/tetratelabs/telemetry"
    "github.com/tetratelabs/telemetry/scope"
)

// meterProvider is your existing OTel MeterProvider (Prometheus, OTLP, etc.)
sink := ziolog.NewOTelSink(meterProvider, "myservice")
telemetry.SetGlobalMetricSink(sink)
scope.UseLogger(ziolog.New(slog.Default()))
```

### 2. Declare metrics in library code

No implementation dependency — library code only imports the telemetry interface:

```go
var (
    clusterLabel telemetry.Label
    reserveOK    telemetry.Metric
    reserveErrs  telemetry.Metric
)

func init() {
    telemetry.ToGlobalMetricSink(func(ms telemetry.MetricSink) {
        clusterLabel = ms.NewLabel("cluster")
        reserveOK    = ms.NewSum("myservice_reserve_total",  "Successful reservations")
        reserveErrs  = ms.NewSum("myservice_reserve_errors", "Reserve errors")
    })
}

var log = scope.Register("myservice", "My service operations")
```

### 3. Log and emit metrics in one call

```go
// Success path
log.Context(ctx).
    Metric(reserveOK.With(clusterLabel.Upsert("openai"))).
    Info("reserve success", "user_id", userID, "tokens", n)
// → slog:  level=INFO  msg="reserve success" scope=myservice user_id=alice tokens=1000
//          trace_id=abc span_id=def  (injected from active OTel span)
// → OTel:  myservice_reserve_total{cluster="openai"} += 1

// Error path
log.Context(ctx).
    Metric(reserveErrs.With(clusterLabel.Upsert("openai"))).
    Error("reserve failed", err, "user_id", userID)
// → slog:  level=ERROR msg="reserve failed" ... err=context deadline exceeded
// → OTel:  myservice_reserve_errors{cluster="openai"} += 1
```

### OTel trace correlation

When a context with an active OTel span is attached via `.Context(ctx)`, `trace_id`
and `span_id` are automatically injected into every log line — no manual extraction:

```
level=INFO msg="reserve success" trace_id=c02b2a3a... span_id=d1449529... cluster=openai
```

The same `trace_id` appears in the OTel trace, making cross-signal correlation trivial.

---

## Sinks

| Sink | When to use |
|------|-------------|
| `NewOTelSink(mp, name)` | Production — backed by OTel `MeterProvider`, exports to Prometheus or OTLP |
| `NewMemSink()` | Tests — in-memory, inspect values with `sink.Snapshot()` |

---

## Testing

### Unit tests

```bash
go test -race ./...
```

Uses `MemSink` — no external deps, instant:

```go
sink := ziolog.NewMemSink()
telemetry.SetGlobalMetricSink(sink)
// ...
assert.Equal(t, float64(1), sink.Snapshot()["myservice_reserve_total"])
```

### E2e tests

Uses an in-process OTLP gRPC sink — no Docker, no sleep, precise assertions on
exact values and labels:

```bash
cd e2e && go test -v -tags e2e -timeout 60s ./...
```

Assertions look like:

```go
// Exact counter value + label
val, ok := sink.WaitForCounter("myservice_reserve_total", "cluster", "openai", 1, 5*time.Second)

// Log body + trace correlation
rec, ok := sink.WaitForLog("reserve success", 5*time.Second)
assert.Equal(t, traceID, rec.Attrs["trace_id"])

// Span by trace ID
span, ok := sink.WaitForSpan(traceID, 5*time.Second)
assert.Equal(t, "quota.Reserve", span.Name)
```

### Manual verification with otel-front

For visual browsing of the full telemetry picture, run with
[otel-front](https://github.com/mesaglio/otel-front):

```bash
# Terminal 1 — start otel-front
docker run --rm -p 8000:8000 -p 4317:4317 -p 4318:4318 \
    ghcr.io/mesaglio/otel-front:latest

# Terminal 2 — run e2e routing to otel-front
cd e2e && E2E_OTEL_FRONT=1 go test -v -tags e2e -timeout 90s ./...

# Open http://localhost:8000
```

You will see the same `trace_id` linking the log record, the metric data point,
and the span — the three signals correlated in one view.

---

## License

Apache 2.0 — see [LICENSE](LICENSE).
