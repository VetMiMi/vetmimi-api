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

	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/redis/go-redis/v9"

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
	err = run(ctx, log, cfg, *mode)
	stop()
	if err != nil {
		log.Error("exit", "err", err)
		os.Exit(1)
	}
}

// run opens and migrates the database for every mode, so a failed migration
// exits non-zero before the api listens and the deploy health check sees a
// failed release.
func run(ctx context.Context, log *slog.Logger, cfg platform.Config, mode string) error {
	if mode != "api" && mode != "worker" {
		return fmt.Errorf("unknown mode %q", mode)
	}
	pool, err := platform.OpenPostgres(ctx, cfg.DatabaseURL)
	if err != nil {
		return err
	}
	defer pool.Close()
	if err := platform.Migrate(ctx, pool); err != nil {
		return err
	}
	rdb, err := platform.OpenRedis(cfg.RedisURL)
	if err != nil {
		return err
	}
	defer rdb.Close()

	if mode == "worker" {
		return runWorker(ctx, log, rdb)
	}
	return runAPI(ctx, log, cfg, pool, rdb)
}

func runAPI(ctx context.Context, log *slog.Logger, cfg platform.Config, pool *pgxpool.Pool, rdb *redis.Client) error {
	srv := newServer(cfg.Port, httpapi.NewRouter(httpapi.Deps{
		PingPostgres: pool.Ping,
		PingRedis:    func(ctx context.Context) error { return rdb.Ping(ctx).Err() },
		Log:          log,
	}))

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

// newServer bounds how long a client may take to send headers and hold an
// idle connection. It sets no WriteTimeout: the video WebSocket (ADR-007)
// stays open for a whole session, and every other route gets its deadline
// from the router instead.
func newServer(port int, h http.Handler) *http.Server {
	return &http.Server{
		Addr:              ":" + strconv.Itoa(port),
		Handler:           h,
		ReadHeaderTimeout: 5 * time.Second,
		IdleTimeout:       120 * time.Second,
		MaxHeaderBytes:    16 << 10,
	}
}
