// Command api runs the VetMiMi HTTP API (default) or its background worker
// (--mode worker). Both modes share one binary and one configuration.
package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"log/slog"
	"net/http"
	"os"
	"os/signal"
	"strconv"
	"syscall"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/go-chi/chi/v5/middleware"

	"github.com/VetMiMi/vetmimi-api/internal/httpapi"
	"github.com/VetMiMi/vetmimi-api/internal/platform"
)

func main() {
	mode := flag.String("mode", "api", "api or worker")
	flag.Parse()

	cfg, err := platform.LoadConfig(os.Getenv)
	if err != nil {
		// LOG_LEVEL may be the broken variable, so report at the default level.
		platform.NewLogger(slog.LevelInfo).Error("exit", "err", err)
		os.Exit(1)
	}
	log := platform.NewLogger(cfg.LogLevel)
	slog.SetDefault(log)

	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer stop()

	switch *mode {
	case "api":
		err = runAPI(ctx, log, cfg)
	case "worker":
		log.Info("worker mode is not implemented yet")
	default:
		err = fmt.Errorf("unknown mode %q", *mode)
	}
	if err != nil {
		log.Error("exit", "err", err)
		os.Exit(1)
	}
}

func runAPI(ctx context.Context, log *slog.Logger, cfg platform.Config) error {
	r := chi.NewRouter()
	// Client IPs are taken from Caddy's X-Forwarded-For in the platform
	// middleware later; chi's RealIP trusts every proxy header and is not used.
	r.Use(middleware.RequestID, middleware.Recoverer)
	r.Use(middleware.Timeout(30 * time.Second))
	r.Mount("/", httpapi.Handler(&httpapi.Server{}))

	srv := &http.Server{
		Addr:              ":" + strconv.Itoa(cfg.Port),
		Handler:           r,
		ReadHeaderTimeout: 5 * time.Second,
	}

	errc := make(chan error, 1)
	go func() {
		log.Info("api listening", "addr", srv.Addr)
		errc <- srv.ListenAndServe()
	}()

	select {
	case err := <-errc:
		if errors.Is(err, http.ErrServerClosed) {
			return nil
		}
		return err
	case <-ctx.Done():
		shutdown, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		return srv.Shutdown(shutdown)
	}
}
