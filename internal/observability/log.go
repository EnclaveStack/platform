// Package observability sets up structured logging and metrics (ADR-011, L3-007 §5–6).
package observability

import (
	"context"
	"io"
	"log/slog"
	"regexp"
	"strings"
)

// sensitiveKey matches attribute keys that must never be logged.
var sensitiveKey = regexp.MustCompile(`(?i)(password|secret|token|value|authorization|cookie|setup_?code)`)

// redact drops sensitive attributes (replacing them with "[redacted]") anywhere in a record.
func redact(_ []string, a slog.Attr) slog.Attr {
	if sensitiveKey.MatchString(a.Key) {
		return slog.String(a.Key, "[redacted]")
	}
	return a
}

// NewLogger builds a JSON (or text, for terminals) logger at level with redaction.
func NewLogger(w io.Writer, level string, text bool) *slog.Logger {
	var lv slog.Level
	switch strings.ToLower(level) {
	case "debug":
		lv = slog.LevelDebug
	case "warn":
		lv = slog.LevelWarn
	case "error":
		lv = slog.LevelError
	default:
		lv = slog.LevelInfo
	}
	opts := &slog.HandlerOptions{Level: lv, ReplaceAttr: redact}
	if text {
		return slog.New(slog.NewTextHandler(w, opts))
	}
	return slog.New(slog.NewJSONHandler(w, opts))
}

type ctxKey struct{}

// WithLogger stores a request-scoped logger in ctx.
func WithLogger(ctx context.Context, l *slog.Logger) context.Context {
	return context.WithValue(ctx, ctxKey{}, l)
}

// Logger returns the request-scoped logger, or fallback.
func Logger(ctx context.Context, fallback *slog.Logger) *slog.Logger {
	if l, ok := ctx.Value(ctxKey{}).(*slog.Logger); ok {
		return l
	}
	return fallback
}

// Discard is a logger that writes nothing (tests).
func Discard() *slog.Logger { return slog.New(slog.NewTextHandler(io.Discard, nil)) }
