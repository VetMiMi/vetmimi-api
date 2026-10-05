package platform

import (
	"log/slog"
	"os"
)

// NewLogger writes JSON lines to stdout, where the container runtime on both
// the live host and Fargate collects them without an agent.
func NewLogger(level slog.Level) *slog.Logger {
	return slog.New(slog.NewJSONHandler(os.Stdout, &slog.HandlerOptions{Level: level}))
}
