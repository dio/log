// Package gcp provides a slog handler that formats log records for Google
// Cloud Logging structured logging.
//
// Cloud Logging ingests JSON written to stdout/stderr on Cloud Run and GKE,
// but only promotes fields to first-class status when the keys match its
// expected names. The default slog.JSONHandler uses the wrong ones:
//
//	slog default   Cloud Logging expects
//	──────────────────────────────────────────────────────────────────
//	level       →  severity  (with GCP severity strings, see below)
//	msg         →  message
//	source      →  logging.googleapis.com/sourceLocation
//
// This package also defines six severity levels that map 1:1 to Cloud
// Logging's severity enum, and remaps the trace_id / span_id fields injected
// by github.com/dio/log into the logging.googleapis.com/* fields that Cloud
// Logging uses to link log entries to Cloud Trace spans.
//
// # Severity levels
//
// Cloud Logging defines more severity levels than slog's four. This package
// exposes them as slog.Level constants that fit into slog's numeric space:
//
//	Level            Value   Cloud Logging severity
//	──────────────────────────────────────────────
//	LevelDebug       -4      DEBUG
//	LevelInfo         0      INFO
//	LevelNotice       2      NOTICE
//	LevelWarning      4      WARNING
//	LevelError        8      ERROR
//	LevelEmergency   12      EMERGENCY
//
// Use them anywhere slog.Level is accepted:
//
//	logger.Log(ctx, gcp.LevelNotice, "quota threshold reached")
//	logger.Log(ctx, gcp.LevelEmergency, "data loss detected")
//
// # Usage
//
//	import (
//	    "log/slog"
//	    "os"
//
//	    log "github.com/dio/log"
//	    "github.com/dio/log/gcp"
//	    "github.com/tetratelabs/telemetry/scope"
//	)
//
//	scope.UseLogger(log.New(slog.New(gcp.NewHandler(os.Stderr, "my-project", nil))))
package gcp

import (
	"io"
	"log/slog"
)

// GCP severity levels as slog.Level constants.
// These fit into slog's numeric int space and map 1:1 to Cloud Logging severity.
// https://cloud.google.com/logging/docs/reference/v2/rest/v2/LogEntry#LogSeverity
const (
	LevelDebug     = slog.Level(-4) // DEBUG
	LevelInfo      = slog.Level(0)  // INFO
	LevelNotice    = slog.Level(2)  // NOTICE    — normal but significant events
	LevelWarning   = slog.Level(4)  // WARNING   — might cause problems
	LevelError     = slog.Level(8)  // ERROR     — likely to cause problems
	LevelEmergency = slog.Level(12) // EMERGENCY — system is unusable
)

// NewHandler returns a slog.Handler that writes GCP Cloud Logging compatible
// JSON to w. It is a slog.NewJSONHandler with ReplaceAttr applied.
//
// projectID is your GCP project ID (e.g. "my-project-123"). It is used to
// build the logging.googleapis.com/trace field:
// "projects/my-project-123/traces/<trace_id>".
//
// If opts is nil, defaults are used. If opts.ReplaceAttr is already set it
// is chained after the GCP remapping so both run.
func NewHandler(w io.Writer, projectID string, opts *slog.HandlerOptions) slog.Handler {
	gcpOpts := &slog.HandlerOptions{}
	if opts != nil {
		*gcpOpts = *opts
	}
	prev := gcpOpts.ReplaceAttr
	gcpOpts.ReplaceAttr = func(groups []string, a slog.Attr) slog.Attr {
		a = replaceAttr(projectID, groups, a)
		if prev != nil {
			a = prev(groups, a)
		}
		return a
	}
	return slog.NewJSONHandler(w, gcpOpts)
}

// ReplaceAttr returns an slog ReplaceAttr function that remaps slog keys to
// GCP Cloud Logging field names and converts OTel trace_id / span_id to the
// logging.googleapis.com/* fields Cloud Logging expects.
//
// Use this when you need to compose with an existing slog.HandlerOptions:
//
//	opts := &slog.HandlerOptions{
//	    AddSource:   true,
//	    ReplaceAttr: gcp.ReplaceAttr("my-project"),
//	}
//	slog.New(slog.NewJSONHandler(os.Stderr, opts))
func ReplaceAttr(projectID string) func([]string, slog.Attr) slog.Attr {
	return func(groups []string, a slog.Attr) slog.Attr {
		return replaceAttr(projectID, groups, a)
	}
}

// replaceAttr is the core remapping logic.
func replaceAttr(projectID string, groups []string, a slog.Attr) slog.Attr {
	// Only remap root-level keys, not inside nested groups.
	if len(groups) > 0 {
		return a
	}

	switch a.Key {

	// slog "level" → GCP "severity" with Cloud Logging severity strings.
	case slog.LevelKey:
		a.Key = "severity"
		if l, ok := a.Value.Any().(slog.Level); ok {
			a.Value = slog.StringValue(levelToSeverity(l))
		}

	// slog "msg" → GCP "message".
	case slog.MessageKey:
		a.Key = "message"

	// slog "source" → GCP "logging.googleapis.com/sourceLocation".
	case slog.SourceKey:
		a.Key = "logging.googleapis.com/sourceLocation"

	// OTel trace_id (injected by github.com/dio/log when a span is active)
	// → GCP "logging.googleapis.com/trace" with the required project prefix.
	case "trace_id":
		a.Key = "logging.googleapis.com/trace"
		a.Value = slog.StringValue("projects/" + projectID + "/traces/" + a.Value.String())

	// OTel span_id → GCP "logging.googleapis.com/spanId".
	case "span_id":
		a.Key = "logging.googleapis.com/spanId"
	}

	return a
}

// levelToSeverity maps a slog.Level to the GCP Cloud Logging severity string.
// The six GCP levels (LevelDebug through LevelEmergency) map exactly.
// Values between defined levels map to the nearest lower severity.
// https://cloud.google.com/logging/docs/reference/v2/rest/v2/LogEntry#LogSeverity
func levelToSeverity(l slog.Level) string {
	switch {
	case l < LevelInfo:
		return "DEBUG"
	case l < LevelNotice:
		return "INFO"
	case l < LevelWarning:
		return "NOTICE"
	case l < LevelError:
		return "WARNING"
	case l < LevelEmergency:
		return "ERROR"
	default:
		return "EMERGENCY"
	}
}
