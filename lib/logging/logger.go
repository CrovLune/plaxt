package logging

import (
	"log/slog"
	"os"
	"strings"
)

// Init configures the global slog default logger.
// LOG_FORMAT: "json" (default) or "text"
// LOG_LEVEL:  "debug", "info" (default), "warn", "error"
func Init() {
	format := strings.ToLower(strings.TrimSpace(os.Getenv("LOG_FORMAT")))
	if format == "" { format = "json" }
	level := strings.ToLower(strings.TrimSpace(os.Getenv("LOG_LEVEL")))
	var lvl slog.Level
	switch level {
	case "debug": lvl = slog.LevelDebug
	case "warn": lvl = slog.LevelWarn
	case "error": lvl = slog.LevelError
	default: lvl = slog.LevelInfo
	}
	var h slog.Handler
	opts := &slog.HandlerOptions{Level: lvl}
	if format == "text" {
		h = slog.NewTextHandler(os.Stdout, opts)
	} else {
		h = slog.NewJSONHandler(os.Stdout, opts)
	}
	slog.SetDefault(slog.New(h))
}