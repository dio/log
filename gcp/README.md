# log/gcp

[![Go Reference](https://pkg.go.dev/badge/github.com/dio/log/gcp.svg)](https://pkg.go.dev/github.com/dio/log/gcp)

A `slog.Handler` that formats log records for [Google Cloud Logging](https://cloud.google.com/logging/docs/structured-logging).

Part of [github.com/dio/log](https://github.com/dio/log). Same `go.mod`, no new dependencies.

---

## The problem

Cloud Run and GKE auto-ingest JSON written to stdout/stderr into Cloud Logging.
But Cloud Logging only promotes fields to first-class status when the keys match
its expected names. The default `slog.JSONHandler` uses the wrong ones:

```
slog default    Cloud Logging expects
─────────────────────────────────────
level        →  severity
msg          →  message
source       →  logging.googleapis.com/sourceLocation
```

Beyond key names, `slog.LevelWarn` must map to `"WARNING"` (not `"WARN"`), and
the OTel `trace_id` / `span_id` fields injected by `github.com/dio/log` must be
reformatted into the `logging.googleapis.com/trace` and `logging.googleapis.com/spanId`
fields that Cloud Logging uses to link log entries to Cloud Trace spans.

This package fixes all of that in one call.

---

## Install

```bash
go get github.com/dio/log/gcp
```

---

## Usage

```go
import (
    "log/slog"
    "os"

    log "github.com/dio/log"
    "github.com/dio/log/gcp"
    "github.com/tetratelabs/telemetry/scope"
)

scope.UseLogger(log.New(slog.New(gcp.NewHandler(os.Stderr, "my-project", nil))))
```

That is the only change needed. Everything else stays the same.

---

## What it remaps

| slog field | Cloud Logging field | Notes |
|---|---|---|
| `level` | `severity` | `WARN` becomes `WARNING` |
| `msg` | `message` | |
| `source` | `logging.googleapis.com/sourceLocation` | only when `AddSource: true` |
| `trace_id` | `logging.googleapis.com/trace` | prefixed with `projects/<projectID>/traces/` |
| `span_id` | `logging.googleapis.com/spanId` | |

The `trace_id` and `span_id` fields are injected automatically by `log.New` whenever
an active OTel span is present in the context. No extra code needed in handlers.

---

## Log entry Cloud Logging receives

```json
{
  "severity": "INFO",
  "message": "request handled",
  "scope": "server",
  "route": "/api/v1/users",
  "logging.googleapis.com/trace": "projects/my-project/traces/4bf92f3577b34da6a3ce929d0e0e4736",
  "logging.googleapis.com/spanId": "00f067aa0ba902b7"
}
```

Cloud Logging promotes `severity` and `message` as first-class fields. The trace
field links the entry to the Cloud Trace span — clicking the trace icon in Logs
Explorer opens Cloud Trace at the exact span.

---

## Composing with existing HandlerOptions

If you already have a `slog.HandlerOptions` (for example with `AddSource` or a
custom `ReplaceAttr`), pass it in. The GCP remapping chains before your existing
`ReplaceAttr`:

```go
opts := &slog.HandlerOptions{
    AddSource: true,
    ReplaceAttr: func(groups []string, a slog.Attr) slog.Attr {
        if a.Key == "password" {
            a.Value = slog.StringValue("[redacted]")
        }
        return a
    },
}
h := gcp.NewHandler(os.Stderr, "my-project", opts)
```

Or use `gcp.ReplaceAttr` directly if you want just the function:

```go
opts := &slog.HandlerOptions{
    ReplaceAttr: gcp.ReplaceAttr("my-project"),
}
slog.New(slog.NewJSONHandler(os.Stderr, opts))
```

---

## Severity mapping

Cloud Logging defines more severity levels than slog's four. This package exposes
them as `slog.Level` constants that fit into slog's numeric space:

| Constant | Value | Cloud Logging severity | When to use |
|---|---|---|---|
| `gcp.LevelDebug` | -4 | `DEBUG` | Detailed diagnostic info |
| `gcp.LevelInfo` | 0 | `INFO` | Normal operation |
| `gcp.LevelNotice` | 2 | `NOTICE` | Normal but significant events |
| `gcp.LevelWarning` | 4 | `WARNING` | Might cause problems |
| `gcp.LevelError` | 8 | `ERROR` | Likely to cause problems |
| `gcp.LevelEmergency` | 12 | `EMERGENCY` | System is unusable |

`gcp.LevelWarning` and `gcp.LevelNotice` sit between slog's `LevelInfo` (0) and
`LevelWarn` (4), so the standard slog `LevelWarn` also maps to `WARNING`. Use the
`gcp.Level*` constants when you need `NOTICE` or `EMERGENCY`:

```go
logger.Log(ctx, gcp.LevelNotice, "quota threshold reached", "used_pct", 80)
logger.Log(ctx, gcp.LevelEmergency, "data loss detected", "table", "users")
```

---

## No new dependencies

This package uses only `log/slog` from the standard library. The `otel/trace`
package is already a dependency of `github.com/dio/log`. Nothing new is added.

---

## License

Apache 2.0. See [LICENSE](../LICENSE).
