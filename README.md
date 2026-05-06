# log

[![Go Reference](https://pkg.go.dev/badge/github.com/dio/log.svg)](https://pkg.go.dev/github.com/dio/log)

A slog-backed implementation of [tetratelabs/telemetry](https://github.com/tetratelabs/telemetry) optimized for OpenTelemetry, with one key guarantee:

> **When you silence logs, metrics still fire.**

## Why

In production you often suppress Info-level logs to reduce noise and cost. With separate `log.Info()` + `counter.Add()` calls, silencing the log silently kills the metric too. With this library, they decouple:

```go
logger.SetLevel(telemetry.LevelError) // Info logs suppressed

logger.Metric(reserveOK).Info("reserve success")
// → log line: absent from output
// → metric:   zia_quota_reserve_total += 1  (always)
```

This is because `RecordContext` fires before the level check — a deliberate ordering that preserves alerting signal regardless of log verbosity.

## Usage

```go
import (
    "log/slog"
    "github.com/dio/log"
    "github.com/tetratelabs/telemetry"
    "github.com/tetratelabs/telemetry/scope"
)

// Wire once in main:
sink := log.NewOTelSink(meterProvider, "myservice")
telemetry.SetGlobalMetricSink(sink)
scope.UseLogger(log.New(slog.Default()))

// Declare metrics in library code (zero impl dep):
var errs telemetry.Metric
func init() {
    telemetry.ToGlobalMetricSink(func(ms telemetry.MetricSink) {
        errs = ms.NewSum("myservice_errors_total", "Errors by cluster")
    })
}

// One call — log + metric + OTel trace correlation:
logger.Context(ctx).Metric(errs).Error("reserve failed", err, "cluster", cluster)
// → slog: level=ERROR msg="reserve failed" trace_id=abc span_id=def cluster=openai err=...
// → OTel: myservice_errors_total{cluster="openai"} += 1
```

## OTel trace correlation

When a context with an active OTel span is attached via `.Context(ctx)`, `trace_id` and `span_id` are automatically injected into every log line. No manual extraction.

## Sinks

| Sink | Use |
|------|-----|
| `NewOTelSink(mp, name)` | Production — backed by OTel `MeterProvider`, flows into Prometheus / OTLP |
| `NewMemSink()` | Tests — in-memory, inspect with `sink.Snapshot()` |

## Testing

Unit tests use `MemSink` — no external deps:

```bash
go test -race ./...
```

E2e tests use an **in-process OTLP gRPC sink** — no Docker, no sleep, precise assertions:

```bash
cd e2e && go test -v -tags e2e -timeout 60s ./...
```

### Manual verification with otel-front

For human browsing of traces, logs, and metrics, start [otel-front](https://github.com/mesaglio/otel-front) and re-run with `E2E_OTEL_FRONT=1`:

```bash
# Terminal 1 — start otel-front
docker run -p 8000:8000 -p 4317:4317 -p 4318:4318 ghcr.io/mesaglio/otel-front:latest

# Terminal 2 — run e2e pointing at otel-front
cd e2e && E2E_OTEL_FRONT=1 go test -v -tags e2e -timeout 90s ./...

# Open http://localhost:8000 — traces, logs, and metrics from the test run
```

otel-front shows:
- **Metrics** → `zia_quota_reserve_total`, `zia_quota_reserve_errors_total` with cluster labels
- **Logs** → `reserve success`, `reserve failed` with trace correlation
- **Traces** → `quota.Reserve` span, same trace_id as the log records


## License

Apache 2.0
