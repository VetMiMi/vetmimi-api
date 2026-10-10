// Command api is one binary with four modes: the HTTP API (default), the
// background worker, create-user (enrol an administrator) and healthcheck.
// main loads the config, opens PostgreSQL and Redis, then runs the mode.
package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"log/slog"
	"os"
	"os/signal"
	"syscall"
	// The distroless image has no zoneinfo, and Australia/Sydney must load anywhere.
	_ "time/tzdata"

	"github.com/redis/go-redis/v9"

	"github.com/VetMiMi/vetmimi-api/internal/config"
	"github.com/VetMiMi/vetmimi-api/internal/postgres"
)

func main() {
	mode := flag.String("mode", "api", "api, worker, create-user or healthcheck")
	var user userFlags
	user.register(flag.CommandLine)
	flag.Parse()
	if *mode == "healthcheck" {
		os.Exit(healthcheck(os.Getenv("PORT")))
	}

	cfg, err := config.Load(os.Getenv)
	if err != nil {
		// LOG_LEVEL may be the broken variable, so report at the default level.
		newLogger(slog.LevelInfo).Error("exit", "err", err)
		os.Exit(1)
	}
	log := newLogger(cfg.LogLevel)
	slog.SetDefault(log)

	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	err = run(ctx, log, cfg, *mode, user)
	stop()
	if err != nil {
		log.Error("exit", "err", err)
		os.Exit(1)
	}
}

// run migrates the database in every mode, so a failed migration exits
// before the api listens and the deploy sees a failed release.
func run(ctx context.Context, log *slog.Logger, cfg config.Config, mode string, user userFlags) error {
	if mode != "api" && mode != "worker" && mode != "create-user" {
		return fmt.Errorf("unknown mode %q", mode)
	}
	pool, err := postgres.Open(ctx, cfg.DatabaseURL)
	if err != nil {
		return err
	}
	defer pool.Close()
	if err := postgres.Migrate(ctx, pool); err != nil {
		return err
	}
	if mode == "create-user" {
		return runCreateUser(ctx, log, cfg, pool, user)
	}

	rdb, err := openRedis(cfg.RedisURL)
	if err != nil {
		return err
	}
	defer rdb.Close()
	if mode == "worker" {
		return runWorker(ctx, log, cfg, pool, rdb)
	}
	return runAPI(ctx, log, cfg, pool, rdb)
}

// newLogger writes JSON lines to stdout, where the container runtime collects them.
func newLogger(level slog.Level) *slog.Logger {
	return slog.New(slog.NewJSONHandler(os.Stdout, &slog.HandlerOptions{Level: level}))
}

// openRedis does not connect, so the api starts while Redis is down and
// /readyz reports it. Errors never quote the URL: it may hold a password.
func openRedis(url string) (*redis.Client, error) {
	opts, err := redis.ParseURL(url)
	if err != nil {
		return nil, errors.New("open redis: the connection URL is invalid")
	}
	return redis.NewClient(opts), nil
}
