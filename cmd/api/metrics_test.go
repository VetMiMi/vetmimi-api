package main

import (
	"encoding/json"
	"log/slog"
	"net/http"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestMetricsServeTheCountersOnTheirOwnAddress(t *testing.T) {
	log := slog.New(slog.DiscardHandler)
	metrics, err := serveMetrics(log, "127.0.0.1:0")
	require.NoError(t, err)
	defer metrics.Close()

	res, err := http.Get("http://" + metrics.Addr + "/debug/vars")
	require.NoError(t, err)
	defer res.Body.Close()
	require.Equal(t, http.StatusOK, res.StatusCode)
	var counters map[string]any
	require.NoError(t, json.NewDecoder(res.Body).Decode(&counters))
	require.Contains(t, counters, "http_requests")

	_, err = serveMetrics(log, metrics.Addr)
	require.ErrorContains(t, err, "address already in use")
}
