package log

import (
	"context"
	"log/slog"
)

// LevelableHandler is a slog.Handler that supports runtime level changes.
// Any handler returned by this package implements this interface.
type LevelableHandler interface {
	slog.Handler
	SetLevel(level slog.Level)
}

var _ LevelableHandler = (*LevelHandler)(nil)

// LevelHandler wraps any slog.Handler with a slog.LevelVar for atomic,
// concurrent-safe runtime level changes. All loggers derived from it via
// WithAttrs and WithGroup share the same level knob.
type LevelHandler struct {
	handler  slog.Handler
	levelVar *slog.LevelVar
}

// NewLevelHandler wraps h with a LevelVar initialised to leveler.Level().
// If h is already a LevelHandler, its inner handler is unwrapped to avoid
// double-wrapping.
func NewLevelHandler(leveler slog.Leveler, h slog.Handler) *LevelHandler {
	if lh, ok := h.(*LevelHandler); ok {
		h = lh.Handler()
	}
	lv := new(slog.LevelVar)
	lv.Set(leveler.Level())
	return &LevelHandler{handler: h, levelVar: lv}
}

// SetLevel changes the active log level. Safe for concurrent use.
func (h *LevelHandler) SetLevel(level slog.Level) { h.levelVar.Set(level) }

// Level returns the current active log level.
func (h *LevelHandler) Level() slog.Level { return h.levelVar.Level() }

// Handler returns the inner slog.Handler.
func (h *LevelHandler) Handler() slog.Handler { return h.handler }

// Enabled implements slog.Handler.
func (h *LevelHandler) Enabled(_ context.Context, level slog.Level) bool {
	return level >= h.levelVar.Level()
}

// Handle implements slog.Handler.
func (h *LevelHandler) Handle(ctx context.Context, r slog.Record) error {
	return h.handler.Handle(ctx, r)
}

// WithAttrs implements slog.Handler. The returned handler shares the same
// LevelVar so SetLevel on any sibling adjusts all of them.
func (h *LevelHandler) WithAttrs(attrs []slog.Attr) slog.Handler {
	return &LevelHandler{handler: h.handler.WithAttrs(attrs), levelVar: h.levelVar}
}

// WithGroup implements slog.Handler.
func (h *LevelHandler) WithGroup(name string) slog.Handler {
	return &LevelHandler{handler: h.handler.WithGroup(name), levelVar: h.levelVar}
}

// SetLevel adjusts the level on a logger whose handler implements
// LevelableHandler. Panics if the handler does not support runtime level
// changes; use NewLevelHandler when building the logger to guarantee this.
func SetLevel(logger *slog.Logger, level slog.Level) *slog.Logger {
	h, ok := logger.Handler().(LevelableHandler)
	if !ok {
		panic("log.SetLevel: handler does not implement LevelableHandler; wrap with log.NewLevelHandler")
	}
	h.SetLevel(level)
	return logger
}
