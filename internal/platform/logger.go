// Package platform wires the external infrastructure the service depends on:
// structured logging, the PostgreSQL connection pool, and the Redis client.
package platform

import (
	"log/slog"
	"os"
	"strings"
)

// NewLogger returns a JSON structured logger at the given level ("debug",
// "info", "warn", "error") writing to stdout.
func NewLogger(level string) *slog.Logger {
	var lvl slog.Level
	switch strings.ToLower(level) {
	case "debug":
		lvl = slog.LevelDebug
	case "warn", "warning":
		lvl = slog.LevelWarn
	case "error":
		lvl = slog.LevelError
	default:
		lvl = slog.LevelInfo
	}
	handler := slog.NewJSONHandler(os.Stdout, &slog.HandlerOptions{Level: lvl})
	return slog.New(handler)
}
