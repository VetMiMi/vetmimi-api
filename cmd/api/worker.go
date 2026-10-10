package main

import (
	"context"
	"log/slog"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/redis/go-redis/v9"

	"github.com/VetMiMi/vetmimi-api/internal/auth"
	"github.com/VetMiMi/vetmimi-api/internal/booking"
	"github.com/VetMiMi/vetmimi-api/internal/comms"
	"github.com/VetMiMi/vetmimi-api/internal/config"
	"github.com/VetMiMi/vetmimi-api/internal/content"
	"github.com/VetMiMi/vetmimi-api/internal/db"
	"github.com/VetMiMi/vetmimi-api/internal/idempotency"
	"github.com/VetMiMi/vetmimi-api/internal/linkedin"
	"github.com/VetMiMi/vetmimi-api/internal/meta"
	"github.com/VetMiMi/vetmimi-api/internal/queue"
	"github.com/VetMiMi/vetmimi-api/internal/secretbox"
	"github.com/VetMiMi/vetmimi-api/internal/settings"
	"github.com/VetMiMi/vetmimi-api/internal/video"
)

const taskCleanup = "platform:cleanup"

// runWorker processes background tasks until ctx is done.
func runWorker(ctx context.Context, log *slog.Logger, cfg config.Config, pool *pgxpool.Pool, rdb *redis.Client) error {
	resend, err := comms.NewResend(cfg.ResendAPIKey, "")
	if err != nil {
		return err
	}
	current, err := settings.Load(ctx, db.New(pool))
	if err != nil {
		return err
	}
	tokens, err := secretbox.New(cfg.TOTPEncryptionKey)
	if err != nil {
		return err
	}

	w := queue.NewWorker(rdb, log)
	tasks := queue.New(rdb, log)
	w.Handle(taskCleanup, func(ctx context.Context, _ []byte) error {
		return cleanup(ctx, log, db.New(pool), time.Now())
	})
	w.Every("@hourly", taskCleanup)
	(&comms.Tasks{
		Pool: pool, Queue: tasks, Resend: resend, From: cfg.EmailFrom, SiteURL: cfg.SiteURL,
		SigningSecret: cfg.SigningSecret, Log: log, Now: time.Now,
	}).Register(w)
	(&booking.Tasks{Pool: pool, Queue: tasks, Log: log, Now: time.Now, Timezone: current.Timezone}).Register(w)
	(&video.Tasks{Pool: pool, Log: log, Now: time.Now}).Register(w)
	(&content.Tasks{
		Pool: pool, Queue: tasks, SiteURL: cfg.SiteURL, RevalidateSecret: cfg.SiteRevalidateSecret,
		Publishers: publishers(cfg, pool, tokens, log), Log: log, Now: time.Now,
	}).Register(w)

	// Rebuild at once any task Redis lost while the worker was down.
	tasks.Enqueue(ctx, queue.Task{Type: comms.TaskSweep}, queue.Task{Type: booking.TaskSweepHolds},
		queue.Task{Type: content.TaskSweep})
	return w.Run(ctx)
}

func publishers(cfg config.Config, pool *pgxpool.Pool, tokens *secretbox.Box, log *slog.Logger) map[string]content.Publisher {
	metaConnector := meta.New(cfg, pool, tokens, log)
	return map[string]content.Publisher{
		"facebook":  metaConnector.PublishFacebook,
		"instagram": metaConnector.PublishInstagram,
		"linkedin":  linkedin.New(cfg, pool, tokens, log).Publish,
	}
}

// cleanup deletes expired sessions and idempotency keys; a repeat deletes nothing.
func cleanup(ctx context.Context, log *slog.Logger, q db.Querier, now time.Time) error {
	sessions, err := auth.DeleteExpiredSessions(ctx, q, now)
	if err != nil {
		return err
	}
	keys, err := idempotency.DeleteExpired(ctx, q, now)
	if err != nil {
		return err
	}
	log.InfoContext(ctx, "expired rows deleted", "sessions", sessions, "idempotency_keys", keys)
	return nil
}
