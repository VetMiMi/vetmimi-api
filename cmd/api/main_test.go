package main

import (
	"context"
	"log/slog"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestNewLoggerHonoursLevel(t *testing.T) {
	log := newLogger(slog.LevelWarn)
	require.False(t, log.Enabled(context.Background(), slog.LevelInfo))
	require.True(t, log.Enabled(context.Background(), slog.LevelWarn))
}

func TestOpenRedisDoesNotQuoteTheURL(t *testing.T) {
	_, err := openRedis("redis://:hunter2-secret@127.0.0.1:notaport/0")
	require.Error(t, err)
	require.NotContains(t, err.Error(), "hunter2-secret")
}
