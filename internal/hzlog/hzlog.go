// Package hzlog configures the process-wide structured logger (slog).
//
// Diagnostics go to stderr as structured logs: JSON under systemd/journald (so
// grafana-alloy can ship them to Loki) and human-readable text on a TTY (dev).
// stdout stays reserved for machine-readable / operator-requested output (CLI-5).
//
// Tunables:
//   - HZ_LOG_LEVEL  debug|info|warn|error   (default info)
//   - HZ_LOG_FORMAT json|text               (default: text on a TTY, JSON otherwise)
package hzlog

import (
	"log/slog"
	"os"
	"strings"
)

// Setup installs the default slog logger. Call once at process start, before any
// logging. Subsequent code uses the slog package-level functions.
func Setup() {
	opts := &slog.HandlerOptions{Level: levelFromEnv()}
	var h slog.Handler
	if jsonFormat() {
		h = slog.NewJSONHandler(os.Stderr, opts)
	} else {
		h = slog.NewTextHandler(os.Stderr, opts)
	}
	slog.SetDefault(slog.New(h))
}

func levelFromEnv() slog.Level {
	switch strings.ToLower(os.Getenv("HZ_LOG_LEVEL")) {
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

// jsonFormat picks JSON unless explicitly set to text, or stderr is a TTY (dev).
func jsonFormat() bool {
	switch strings.ToLower(os.Getenv("HZ_LOG_FORMAT")) {
	case "json":
		return true
	case "text":
		return false
	}
	return !stderrIsTTY()
}

func stderrIsTTY() bool {
	fi, err := os.Stderr.Stat()
	return err == nil && fi.Mode()&os.ModeCharDevice != 0
}
