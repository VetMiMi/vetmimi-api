package platform

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

// The live host has 2 GB of RAM shared by the api, the worker and
// PostgreSQL itself; ten connections per process is plenty for tens of
// appointments a month.
const (
	postgresMaxConns       = 10
	postgresConnectTimeout = 5 * time.Second
)

// OpenPostgres opens a pool on url and pings it, so a wrong host or password
// stops start-up instead of the first request. Errors never quote the URL,
// which carries the password and is logged; pgx's connect errors name the
// host and user only.
func OpenPostgres(ctx context.Context, url string) (*pgxpool.Pool, error) {
	cfg, err := pgxpool.ParseConfig(url)
	if err != nil {
		return nil, errors.New("open postgres: the connection URL is invalid")
	}
	cfg.MaxConns = postgresMaxConns
	cfg.ConnConfig.ConnectTimeout = postgresConnectTimeout

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

// Migrate applies every pending migration in the migrations package. The api
// and worker containers start together on a deploy; goose's session locker
// holds a PostgreSQL advisory lock for the run, so the second process waits
// and then finds nothing left to apply.
func Migrate(ctx context.Context, pool *pgxpool.Pool) error {
	// Poll for the lock every second, for up to five minutes.
	locker, err := lock.NewPostgresSessionLocker(lock.WithLockTimeout(1, 300))
	if err != nil {
		return fmt.Errorf("migrate: %w", err)
	}
	// Closing this *sql.DB leaves the pool open; pgx's connector only
	// borrows from it.
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
