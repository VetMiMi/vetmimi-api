package main

import (
	"context"
	"log/slog"
	"net/http"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

func TestServerLimits(t *testing.T) {
	srv := newServer(8080, http.NotFoundHandler())
	require.Equal(t, ":8080", srv.Addr)
	require.Equal(t, 5*time.Second, srv.ReadHeaderTimeout)
	require.Equal(t, 120*time.Second, srv.IdleTimeout)
	require.Equal(t, 16<<10, srv.MaxHeaderBytes)
	require.Zero(t, srv.WriteTimeout, "the video WebSocket outlives any write timeout")
	require.Zero(t, srv.ReadTimeout, "a read timeout would cut long uploads and the WebSocket")
}

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
