// Package postgres opens the PostgreSQL pool and applies the migrations.
package postgres

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/jackc/pgx/v5/stdlib"
	"github.com/pressly/goose/v3"
	"github.com/pressly/goose/v3/lock"

	"github.com/VetMiMi/vetmimi-api/migrations"
)

// The live host has 2 GB of RAM shared by the api, the worker and PostgreSQL.
const (
	maxConns       = 10
	connectTimeout = 5 * time.Second
)

// Open opens a pool on url and pings it, so a wrong host or password stops
// start-up. Errors never quote the URL: it carries the password.
func Open(ctx context.Context, url string) (*pgxpool.Pool, error) {
	cfg, err := pgxpool.ParseConfig(url)
	if err != nil {
		return nil, errors.New("open postgres: the connection URL is invalid")
	}
	cfg.MaxConns = maxConns
	cfg.ConnConfig.ConnectTimeout = connectTimeout

	pool, err := pgxpool.NewWithConfig(ctx, cfg)
	if err != nil {
		return nil, fmt.Errorf("open postgres: %w", err)
	}
	if err := pool.Ping(ctx); err != nil {
		pool.Close()
		return nil, fmt.Errorf("open postgres: %w", err)
	}
	return pool, nil
}

// Migrate applies every pending migration. The api and the worker both call
// it on start; an advisory lock makes the second wait and find nothing to do.
func Migrate(ctx context.Context, pool *pgxpool.Pool) error {
	// Poll for the lock every second, for up to five minutes.
	locker, err := lock.NewPostgresSessionLocker(lock.WithLockTimeout(1, 300))
	if err != nil {
		return fmt.Errorf("migrate: %w", err)
	}
	// Closing this *sql.DB leaves the pool open.
	provider, err := goose.NewProvider(goose.DialectPostgres, stdlib.OpenDBFromPool(pool), migrations.FS,
		goose.WithSessionLocker(locker))
	if err != nil {
		return fmt.Errorf("migrate: %w", err)
	}
	defer provider.Close()

	if _, err := provider.Up(ctx); err != nil {
		return fmt.Errorf("migrate: %w", err)
	}
	return nil
}
