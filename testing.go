package log

import (
	"io"
	"log/slog"
	"math"
	"strings"
	"testing"
)

// TestLogger returns a *slog.Logger for use in tests.
//
// It logs everything down to the lowest possible level (all Debug and below),
// writes via tb.Log so output is tied to the test, visible only on failure
// or when -v is passed, never mixed into stdout.
//
// Source locations are included so failing test output shows exactly which
// log call fired.
//
//	func TestSomething(t *testing.T) {
//	    logger := log.TestLogger(t)
//	    scope.UseLogger(log.New(logger))
//	    // ... exercise code ...
//	}
func TestLogger(tb testing.TB) *slog.Logger {
	tb.Helper()

	// math.MinInt captures every possible slog level, including custom ones
	// below LevelDebug (-4). Nothing is filtered.
	level := slog.Level(math.MinInt)

	return slog.New(NewLevelHandler(level, slog.NewTextHandler(
		&testingWriter{tb: tb},
		&slog.HandlerOptions{
			AddSource: true,
			Level:     level,
			ReplaceAttr: func(groups []string, a slog.Attr) slog.Attr {
				// Drop time: tb.Log already timestamps each line.
				if a.Key == slog.TimeKey {
					return slog.Attr{}
				}
				return a
			},
		},
	)))
}

// testingWriter routes slog output through tb.Log.
// When -v is not set, Write is a no-op so passing tests stay silent.
var _ io.Writer = (*testingWriter)(nil)

type testingWriter struct{ tb testing.TB }

func (w *testingWriter) Write(b []byte) (int, error) {
	if !testing.Verbose() {
		return len(b), nil
	}
	// tb.Log adds a newline; strip the trailing one slog already appended.
	w.tb.Log(strings.TrimRight(string(b), "\n"))
	return len(b), nil
}
