// Package gcp provides a slog handler that formats log records for Google
// Cloud Logging structured logging.
//
// Cloud Logging ingests JSON written to stdout/stderr on Cloud Run and GKE,
// but only promotes fields to first-class status when the keys match its
// expected names. The default slog.JSONHandler uses the wrong keys:
//
//	slog default   GCP Cloud Logging expects
//	──────────────────────────────────────────
//	level       →  severity  (+ "WARNING" not "WARN", "CRITICAL" for fatal)
//	msg         →  message
//	source      →  logging.googleapis.com/sourceLocation
//
// This package provides ReplaceAttr and NewHandler that fix those mappings.
// It also remaps the trace_id and span_id fields injected by github.com/dio/log
// into the logging.googleapis.com/trace and logging.googleapis.com/spanId
// fields that Cloud Logging uses to link log entries to Cloud Trace spans.
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
//
// Log output on Cloud Logging:
//
//	{
//	  "severity": "INFO",
//	  "message": "request handled",
//	  "logging.googleapis.com/trace": "projects/my-project/traces/4bf92f35...",
//	  "logging.googleapis.com/spanId": "00f067aa0ba902b7"
//	}
//
// Cloud Logging auto-links the entry to the Cloud Trace span when the trace
// field is present.
package gcp

import (
	"io"
	"log/slog"
)

// NewHandler returns a slog.Handler that writes GCP Cloud Logging compatible
// JSON to w. It is a slog.NewJSONHandler with ReplaceAttr applied.
//
// projectID is your GCP project ID (e.g. "my-project-123"). It is used to
// build the logging.googleapis.com/trace field value:
// "projects/my-project-123/traces/<trace_id>".
//
// If opts is nil, defaults are used. If opts.ReplaceAttr is already set, it
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
	// Only remap root-level keys (not inside nested groups).
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

// levelToSeverity maps slog.Level to the GCP Cloud Logging severity string.
// https://cloud.google.com/logging/docs/reference/v2/rest/v2/LogEntry#LogSeverity
func levelToSeverity(l slog.Level) string {
	switch {
	case l < slog.LevelInfo:
		return "DEBUG"
	case l < slog.LevelWarn:
		return "INFO"
	case l < slog.LevelError:
		return "WARNING" // GCP uses WARNING, not WARN
	default:
		return "ERROR"
	}
}
