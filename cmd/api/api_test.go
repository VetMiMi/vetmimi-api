package main

import (
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
