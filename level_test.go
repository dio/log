package log

import (
	"bytes"
	"log/slog"
	"math"
	"strings"
	"testing"
)

func TestLevelHandler_setLevel(t *testing.T) {
	var buf bytes.Buffer
	h := NewLevelHandler(slog.LevelInfo, slog.NewTextHandler(&buf, nil))
	logger := slog.New(h)

	// Debug is filtered at Info level.
	logger.Debug("should not appear")
	if buf.Len() != 0 {
		t.Errorf("expected no output, got: %s", buf.String())
	}

	// Change to Debug at runtime.
	SetLevel(logger, slog.LevelDebug)
	logger.Debug("should appear")
	if !strings.Contains(buf.String(), "should appear") {
		t.Errorf("expected debug output after SetLevel, got: %s", buf.String())
	}
}

func TestLevelHandler_derivedLoggersShareLevel(t *testing.T) {
	var buf bytes.Buffer
	h := NewLevelHandler(slog.LevelInfo, slog.NewTextHandler(&buf, nil))
	root := slog.New(h)

	// Derived logger via With, shares the same LevelVar.
	derived := root.With("key", "val")

	SetLevel(root, slog.LevelDebug)

	derived.Debug("derived debug")
	if !strings.Contains(buf.String(), "derived debug") {
		t.Errorf("derived logger did not pick up level change: %s", buf.String())
	}
}

func TestSetLevel_panicOnNonLevelable(t *testing.T) {
	defer func() {
		if r := recover(); r == nil {
			t.Error("expected panic for non-LevelableHandler")
		}
	}()

	var buf bytes.Buffer
	logger := slog.New(slog.NewTextHandler(&buf, nil))
	SetLevel(logger, slog.LevelDebug)
}

func TestTestLogger_debugVisible(t *testing.T) {
	logger := TestLogger(t)

	if !logger.Enabled(nil, slog.Level(math.MinInt)) {
		t.Error("TestLogger should enable all levels")
	}
	if !logger.Enabled(nil, slog.LevelDebug) {
		t.Error("TestLogger should enable Debug")
	}

	// Should not panic.
	logger.Debug("debug from test")
	logger.Info("info from test")
	logger.Warn("warn from test")
	logger.Error("error from test")
}
