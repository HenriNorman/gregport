// Package logging builds the process-wide structured logger and carries a
// request-scoped logger through context.
package logging

import (
	"context"
	"fmt"
	"io"
	"log/slog"
	"os"
	"strings"
)

type ctxKey struct{}

// defaultLogger is used when a context has no logger attached. It discards
// output so that packages and tests never fall back to unstructured logging.
var defaultLogger = slog.New(slog.NewJSONHandler(io.Discard, nil))

// New builds a JSON logger writing to stdout at the given level.
func New(level string) (*slog.Logger, error) {
	lvl, err := ParseLevel(level)
	if err != nil {
		return nil, err
	}
	return NewWithWriter(os.Stdout, lvl), nil
}

// NewWithWriter builds a JSON logger writing to w. Tests use it to capture
// output without touching stdout.
func NewWithWriter(w io.Writer, level slog.Level) *slog.Logger {
	return slog.New(slog.NewJSONHandler(w, &slog.HandlerOptions{Level: level}))
}

// ParseLevel maps a config string to a slog level.
func ParseLevel(s string) (slog.Level, error) {
	switch strings.ToLower(strings.TrimSpace(s)) {
	case "", "info":
		return slog.LevelInfo, nil
	case "debug":
		return slog.LevelDebug, nil
	case "warn", "warning":
		return slog.LevelWarn, nil
	case "error":
		return slog.LevelError, nil
	default:
		return 0, fmt.Errorf("invalid log level %q: want debug, info, warn or error", s)
	}
}

// SetDefault sets both the package fallback and slog's process default.
func SetDefault(l *slog.Logger) {
	if l == nil {
		return
	}
	defaultLogger = l
	slog.SetDefault(l)
}

// WithLogger stores l in ctx.
func WithLogger(ctx context.Context, l *slog.Logger) context.Context {
	return context.WithValue(ctx, ctxKey{}, l)
}

// FromContext returns the logger stored in ctx, or the package default.
func FromContext(ctx context.Context) *slog.Logger {
	if l, ok := ctx.Value(ctxKey{}).(*slog.Logger); ok && l != nil {
		return l
	}
	return defaultLogger
}
