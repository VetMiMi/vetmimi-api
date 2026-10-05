package main

import (
	"context"
	"log/slog"

	"github.com/redis/go-redis/v9"

	"github.com/VetMiMi/vetmimi-api/internal/platform"
)

// runWorker processes background tasks until ctx is done. Domain packages
// register their task handlers and periodic tasks here as they arrive.
func runWorker(ctx context.Context, log *slog.Logger, rdb *redis.Client) error {
	return platform.NewWorker(rdb, log).Run(ctx)
}
