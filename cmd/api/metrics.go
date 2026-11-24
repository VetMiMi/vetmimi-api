package main

import (
	"errors"
	"expvar"
	"fmt"
	"log/slog"
	"net"
	"net/http"
	"time"
)

// serveMetrics serves the expvar counters at /debug/vars on a server of its own.
// It listens before returning, so an address already in use stops start-up.
func serveMetrics(log *slog.Logger, addr string) (*http.Server, error) {
	listener, err := net.Listen("tcp", addr)
	if err != nil {
		return nil, fmt.Errorf("metrics: %w", err)
	}
	mux := http.NewServeMux()
	mux.Handle("GET /debug/vars", expvar.Handler())
	server := &http.Server{
		Addr:              listener.Addr().String(),
		Handler:           mux,
		ReadHeaderTimeout: 5 * time.Second,
	}
	go func() {
		log.Info("metrics listening", "addr", server.Addr)
		err := server.Serve(listener)
		if !errors.Is(err, http.ErrServerClosed) {
			log.Error("metrics_stopped", "err", err)
		}
	}()
	return server, nil
}
