package config

import (
	"context"
	"log/slog"
	"sync/atomic"
)

// Runtime publishes immutable snapshots. Consumers capture one snapshot at the
// start of an operation; updates never mutate configuration held by active work.
type Runtime struct{ current atomic.Pointer[Config] }

func NewRuntime(c Config) *Runtime { r := &Runtime{}; r.Store(c); return r }
func (r *Runtime) Current() Config { return *r.current.Load() }
func (r *Runtime) Store(c Config)  { r.current.Store(&c) }

// RuntimeLogHandler keeps derived loggers (With/WithGroup) on the current policy.
type RuntimeLogHandler struct {
	Runtime    *Runtime
	JSON, Text slog.Handler
}

func (h RuntimeLogHandler) Enabled(_ context.Context, level slog.Level) bool {
	minimum := slog.LevelInfo
	switch h.Runtime.Current().LogLevel {
	case "debug":
		minimum = slog.LevelDebug
	case "warn":
		minimum = slog.LevelWarn
	case "error":
		minimum = slog.LevelError
	}
	return level >= minimum
}
func (h RuntimeLogHandler) Handle(ctx context.Context, record slog.Record) error {
	if h.Runtime.Current().LogFormat == "json" {
		return h.JSON.Handle(ctx, record)
	}
	return h.Text.Handle(ctx, record)
}
func (h RuntimeLogHandler) WithAttrs(a []slog.Attr) slog.Handler {
	h.JSON = h.JSON.WithAttrs(a)
	h.Text = h.Text.WithAttrs(a)
	return h
}
func (h RuntimeLogHandler) WithGroup(name string) slog.Handler {
	h.JSON = h.JSON.WithGroup(name)
	h.Text = h.Text.WithGroup(name)
	return h
}
