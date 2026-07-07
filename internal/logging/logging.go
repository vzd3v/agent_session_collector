package logging

import (
	"io"
	"log/slog"
	"strings"
)

type Config struct {
	Format string
	Level  string
}

func New(w io.Writer, cfg Config) *slog.Logger {
	level := parseLevel(cfg.Level)
	opts := &slog.HandlerOptions{Level: level}
	if strings.EqualFold(cfg.Format, "text") {
		return slog.New(slog.NewTextHandler(w, opts))
	}
	return slog.New(slog.NewJSONHandler(w, opts))
}

func parseLevel(value string) slog.Level {
	switch strings.ToLower(strings.TrimSpace(value)) {
	case "debug":
		return slog.LevelDebug
	case "warn", "warning":
		return slog.LevelWarn
	case "error":
		return slog.LevelError
	default:
		return slog.LevelInfo
	}
}
