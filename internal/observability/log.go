package observability

import (
	"context"
	"io"
	"log/slog"
	"os"
	"sort"
	"strings"

	"github.com/astronaut808/awg-forge/internal/redact"
)

type Logger struct {
	logger *slog.Logger
}

func New(level string) *Logger {
	if strings.TrimSpace(level) == "" {
		return NewWithWriter("error", io.Discard)
	}
	return NewWithWriter(level, os.Stderr)
}

func NewWithWriter(level string, writer io.Writer) *Logger {
	return &Logger{logger: slog.New(slog.NewJSONHandler(writer, &slog.HandlerOptions{Level: parseLevel(level)}))}
}

func (l *Logger) Log(ctx context.Context, level slog.Level, component, event, message string, fields map[string]any, err error) {
	if l == nil || l.logger == nil {
		return
	}
	attrs := []slog.Attr{
		slog.String("component", strings.TrimSpace(component)),
		slog.String("event", strings.TrimSpace(event)),
	}
	clean := redact.Fields(fields)
	keys := make([]string, 0, len(clean))
	for key := range clean {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	for _, key := range keys {
		attrs = append(attrs, slog.Any(key, clean[key]))
	}
	if err != nil {
		attrs = append(attrs, slog.String("error", redact.String(err.Error())))
	}
	l.logger.LogAttrs(ctx, level, redact.String(message), attrs...)
}

func (l *Logger) Enabled(level slog.Level) bool {
	return l != nil && l.logger != nil && l.logger.Enabled(context.Background(), level)
}

func (l *Logger) Debug(ctx context.Context, component, event, message string, fields map[string]any) {
	l.Log(ctx, slog.LevelDebug, component, event, message, fields, nil)
}

func (l *Logger) Info(ctx context.Context, component, event, message string, fields map[string]any) {
	l.Log(ctx, slog.LevelInfo, component, event, message, fields, nil)
}

func (l *Logger) Warn(ctx context.Context, component, event, message string, fields map[string]any, err error) {
	l.Log(ctx, slog.LevelWarn, component, event, message, fields, err)
}

func (l *Logger) Error(ctx context.Context, component, event, message string, fields map[string]any, err error) {
	l.Log(ctx, slog.LevelError, component, event, message, fields, err)
}

func parseLevel(value string) slog.Level {
	switch strings.ToLower(strings.TrimSpace(value)) {
	case "debug":
		return slog.LevelDebug
	case "warn":
		return slog.LevelWarn
	case "error":
		return slog.LevelError
	default:
		return slog.LevelInfo
	}
}
