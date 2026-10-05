package platform

import (
	"context"
	"log/slog"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestNewLoggerHonoursLevel(t *testing.T) {
	log := NewLogger(slog.LevelWarn)
	require.False(t, log.Enabled(context.Background(), slog.LevelInfo))
	require.True(t, log.Enabled(context.Background(), slog.LevelWarn))
}
