package gcp_test

import (
	"bytes"
	"encoding/json"
	"log/slog"
	"strings"
	"testing"

	"github.com/dio/log/gcp"
)

func TestNewHandler_fieldRemapping(t *testing.T) {
	var buf bytes.Buffer
	h := gcp.NewHandler(&buf, "my-project", nil)
	logger := slog.New(h)

	logger.Info("request handled", "route", "/hello")

	var entry map[string]any
	if err := json.Unmarshal(buf.Bytes(), &entry); err != nil {
		t.Fatalf("unmarshal: %v\nraw: %s", err, buf.String())
	}

	// "msg" → "message"
	if entry["message"] != "request handled" {
		t.Errorf("message: got %v", entry["message"])
	}
	if _, ok := entry["msg"]; ok {
		t.Error("msg key must not be present")
	}

	// "level" → "severity"
	if entry["severity"] != "INFO" {
		t.Errorf("severity: got %v", entry["severity"])
	}
	if _, ok := entry["level"]; ok {
		t.Error("level key must not be present")
	}

	// custom field preserved
	if entry["route"] != "/hello" {
		t.Errorf("route: got %v", entry["route"])
	}
}

func TestNewHandler_severityMapping(t *testing.T) {
	cases := []struct {
		level    slog.Level
		expected string
	}{
		{gcp.LevelDebug, "DEBUG"},
		{gcp.LevelInfo, "INFO"},
		{gcp.LevelNotice, "NOTICE"},
		{gcp.LevelWarning, "WARNING"},
		{gcp.LevelError, "ERROR"},
		{gcp.LevelEmergency, "EMERGENCY"},
		// slog compat: LevelWarn (4) == LevelWarning
		{slog.LevelWarn, "WARNING"},
		// slog compat: LevelError (8) == LevelError
		{slog.LevelError, "ERROR"},
	}

	for _, tc := range cases {
		var buf bytes.Buffer
		h := gcp.NewHandler(&buf, "proj", &slog.HandlerOptions{Level: gcp.LevelDebug})
		slog.New(h).Log(nil, tc.level, "test")

		var entry map[string]any
		_ = json.Unmarshal(buf.Bytes(), &entry)
		if entry["severity"] != tc.expected {
			t.Errorf("level %v: want severity %q, got %v", tc.level, tc.expected, entry["severity"])
		}
	}
}

func TestNewHandler_traceCorrelation(t *testing.T) {
	var buf bytes.Buffer
	h := gcp.NewHandler(&buf, "my-project", nil)

	// Simulate what log.New injects when a span is active.
	slog.New(h).Info("msg",
		"trace_id", "4bf92f3577b34da6a3ce929d0e0e4736",
		"span_id", "00f067aa0ba902b7",
	)

	var entry map[string]any
	if err := json.Unmarshal(buf.Bytes(), &entry); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}

	want := "projects/my-project/traces/4bf92f3577b34da6a3ce929d0e0e4736"
	if entry["logging.googleapis.com/trace"] != want {
		t.Errorf("trace: got %v, want %s", entry["logging.googleapis.com/trace"], want)
	}
	if entry["logging.googleapis.com/spanId"] != "00f067aa0ba902b7" {
		t.Errorf("spanId: got %v", entry["logging.googleapis.com/spanId"])
	}
	if _, ok := entry["trace_id"]; ok {
		t.Error("trace_id key must be replaced, not kept")
	}
	if _, ok := entry["span_id"]; ok {
		t.Error("span_id key must be replaced, not kept")
	}
}

func TestReplaceAttr_chainsWithExisting(t *testing.T) {
	var buf bytes.Buffer
	opts := &slog.HandlerOptions{
		ReplaceAttr: func(groups []string, a slog.Attr) slog.Attr {
			// Custom: redact a field.
			if a.Key == "password" {
				a.Value = slog.StringValue("[redacted]")
			}
			return a
		},
	}
	h := gcp.NewHandler(&buf, "proj", opts)
	slog.New(h).Info("login", "password", "secret123")

	var entry map[string]any
	_ = json.Unmarshal(buf.Bytes(), &entry)

	if entry["severity"] != "INFO" {
		t.Errorf("severity: got %v", entry["severity"])
	}
	if entry["password"] != "[redacted]" {
		t.Errorf("password: got %v, want [redacted]", entry["password"])
	}
}

func TestNewHandler_jsonOutput(t *testing.T) {
	var buf bytes.Buffer
	h := gcp.NewHandler(&buf, "my-project", nil)
	slog.New(h).Warn("high latency", "ms", 350)

	out := buf.String()
	if !strings.Contains(out, `"severity":"WARNING"`) {
		t.Errorf("missing severity: %s", out)
	}
	if !strings.Contains(out, `"message":"high latency"`) {
		t.Errorf("missing message: %s", out)
	}
}

func TestLevelNotice(t *testing.T) {
	var buf bytes.Buffer
	h := gcp.NewHandler(&buf, "proj", &slog.HandlerOptions{Level: gcp.LevelDebug})
	slog.New(h).Log(nil, gcp.LevelNotice, "quota threshold reached", "used", 80)

	var entry map[string]any
	_ = json.Unmarshal(buf.Bytes(), &entry)

	if entry["severity"] != "NOTICE" {
		t.Errorf("severity: got %v, want NOTICE", entry["severity"])
	}
	if entry["message"] != "quota threshold reached" {
		t.Errorf("message: got %v", entry["message"])
	}
}

func TestLevelEmergency(t *testing.T) {
	var buf bytes.Buffer
	h := gcp.NewHandler(&buf, "proj", &slog.HandlerOptions{Level: gcp.LevelDebug})
	slog.New(h).Log(nil, gcp.LevelEmergency, "data loss detected")

	var entry map[string]any
	_ = json.Unmarshal(buf.Bytes(), &entry)

	if entry["severity"] != "EMERGENCY" {
		t.Errorf("severity: got %v, want EMERGENCY", entry["severity"])
	}
}
