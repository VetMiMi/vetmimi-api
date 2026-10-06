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
	"github.com/VetMiMi/vetmimi-api/internal/content"
	"github.com/VetMiMi/vetmimi-api/internal/db"
	"github.com/VetMiMi/vetmimi-api/internal/meta"
	"github.com/VetMiMi/vetmimi-api/internal/platform"
	"github.com/VetMiMi/vetmimi-api/internal/platform/idempotency"
	"github.com/VetMiMi/vetmimi-api/internal/platform/settings"
	"github.com/VetMiMi/vetmimi-api/internal/video"
)

// taskCleanup deletes what has expired (docs/architecture.md, "Background
// jobs"). Deleting twice deletes nothing, so a repeated run is harmless.
const taskCleanup = "platform:cleanup"

// runWorker processes background tasks until ctx is done. Domain packages
// register their task handlers and periodic tasks here as they arrive.
func runWorker(ctx context.Context, log *slog.Logger, cfg platform.Config, pool *pgxpool.Pool, rdb *redis.Client) error {
	w := platform.NewWorker(rdb, log)
	w.Handle(taskCleanup, func(ctx context.Context, _ []byte) error {
		return cleanup(ctx, log, db.New(pool), time.Now())
	})
	w.Every("@hourly", taskCleanup)

	resend, err := comms.NewResend(cfg.ResendAPIKey, "")
	if err != nil {
		return err
	}
	cur, err := settings.Load(ctx, db.New(pool))
	if err != nil {
		return err
	}
	queue := platform.NewQueue(rdb, log)
	(&comms.Tasks{
		Pool: pool, Queue: queue, Resend: resend, From: cfg.EmailFrom, SiteURL: cfg.SiteURL,
		SigningSecret: cfg.SigningSecret, Log: log, Now: time.Now,
	}).Register(w)
	(&booking.Tasks{Pool: pool, Queue: queue, Log: log, Now: time.Now, Timezone: cur.Timezone}).Register(w)
	(&video.Tasks{Pool: pool, Log: log, Now: time.Now}).Register(w)
	tokens, err := auth.NewTOTP(cfg.TOTPEncryptionKey, time.Now)
	if err != nil {
		return err
	}
	connector := meta.New(cfg, pool, tokens, log)
	(&content.Tasks{Pool: pool, Queue: queue, SiteURL: cfg.SiteURL, RevalidateSecret: cfg.SiteRevalidateSecret,
		Publishers: map[string]content.Publisher{
			"facebook": connector.PublishFacebook, "instagram": connector.PublishInstagram,
		},
		Log: log, Now: time.Now}).Register(w)
	// Rebuild at once any task Redis lost while the worker was down.
	queue.Enqueue(ctx, platform.Task{Type: comms.TaskSweep}, platform.Task{Type: booking.TaskSweepHolds},
		platform.Task{Type: content.TaskSweep})
	return w.Run(ctx)
}

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
