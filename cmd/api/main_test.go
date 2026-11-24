package main

import (
	"bytes"
	"context"
	"encoding/json"
	"log/slog"
	"net"
	"testing"
	"time"

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

func TestRedisLoggerWritesJSON(t *testing.T) {
	var logs bytes.Buffer
	logger := redisLogger{log: slog.New(slog.NewJSONHandler(&logs, nil))}

	logger.Printf(context.Background(), "redis: connection pool: failed to dial after %d attempts: %v", 5, "connection refused")

	var line map[string]any
	require.NoError(t, json.Unmarshal(logs.Bytes(), &line), logs.String())
	require.Equal(t, "redis: connection pool: failed to dial after 5 attempts: connection refused", line["msg"])
	require.Equal(t, "WARN", line["level"])
}

func TestRedisGivesUpAtTheDeadlineWhenItNeverAnswers(t *testing.T) {
	// The kernel completes the handshake even though nothing accepts, so the client connects and waits.
	silent, err := net.Listen("tcp", "127.0.0.1:0")
	require.NoError(t, err)
	defer silent.Close()
	rdb, err := openRedis("redis://" + silent.Addr().String())
	require.NoError(t, err)
	defer rdb.Close()

	ctx, cancel := context.WithTimeout(context.Background(), 300*time.Millisecond)
	defer cancel()
	start := time.Now()
	require.Error(t, rdb.Ping(ctx).Err())
	require.Less(t, time.Since(start), time.Second, "a silent Redis held the call past its deadline")
}
