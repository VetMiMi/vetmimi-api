package main

import (
	"context"
	"errors"
	"log/slog"
	"net/http"
	"strconv"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/redis/go-redis/v9"

	"github.com/VetMiMi/vetmimi-api/internal/assistant"
	"github.com/VetMiMi/vetmimi-api/internal/auth"
	"github.com/VetMiMi/vetmimi-api/internal/config"
	"github.com/VetMiMi/vetmimi-api/internal/httpapi"
	"github.com/VetMiMi/vetmimi-api/internal/linkedin"
	"github.com/VetMiMi/vetmimi-api/internal/media"
	"github.com/VetMiMi/vetmimi-api/internal/meta"
	"github.com/VetMiMi/vetmimi-api/internal/queue"
	"github.com/VetMiMi/vetmimi-api/internal/ratelimit"
	"github.com/VetMiMi/vetmimi-api/internal/video"
)

// runAPI serves HTTP until ctx is done.
func runAPI(ctx context.Context, log *slog.Logger, cfg config.Config, pool *pgxpool.Pool, rdb *redis.Client) error {
	codes, err := auth.NewTOTP(cfg.TOTPEncryptionKey, time.Now)
	if err != nil {
		return err
	}
	sessions, err := auth.NewSessions(pool, codes, auth.NewLockout(rdb, "", time.Now), time.Now)
	if err != nil {
		return err
	}
	hub := video.NewHub(pool, log, time.Now)
	srv := newServer(cfg.Port, httpapi.NewRouter(httpapi.Deps{
		PingPostgres:   pool.Ping,
		PingRedis:      func(ctx context.Context) error { return rdb.Ping(ctx).Err() },
		Log:            log,
		ServiceKey:     cfg.ServiceKey,
		RateLimits:     httpapi.NewRateLimits(ratelimit.New(rdb, "", time.Now), log, time.Now),
		Sessions:       sessions,
		Pool:           pool,
		Queue:          queue.New(rdb, log),
		SigningSecret:  cfg.SigningSecret,
		PublicAPIURL:   cfg.PublicAPIURL,
		TURNHost:       cfg.TURNHost,
		TURNSecret:     cfg.TURNSecret,
		SiteURL:        cfg.SiteURL,
		Hub:            hub,
		Media:          media.NewStore(cfg),
		MediaPublicURL: cfg.MediaPublicURL,
		Meta:           meta.New(cfg, pool, codes.Box, log),
		LinkedIn:       linkedin.New(cfg, pool, codes.Box, log),
		Assistant:      assistant.New(cfg, log),
		Now:            time.Now,
	}))
	if cfg.TURNHost == "" {
		// Development only: production refuses to start without TURN.
		log.Warn("turn_disabled", "detail", "TURN_HOST is empty; room tickets offer STUN only")
	}
	return serve(ctx, log, srv, hub)
}

// serve runs srv and the video hub until ctx is done, then shuts both down.
func serve(ctx context.Context, log *slog.Logger, srv *http.Server, hub *video.Hub) error {
	hubDone := make(chan struct{})
	go func() {
		hub.Run(ctx)
		close(hubDone)
	}()
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
		// Shutdown does not wait for hijacked connections, so the hub first
		// tells every video participant to reconnect.
		<-hubDone
		shutdown, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		return srv.Shutdown(shutdown)
	}
}

// newServer sets no WriteTimeout: the video WebSocket stays open for a whole
// session, and every other route gets its deadline from the router.
func newServer(port int, h http.Handler) *http.Server {
	return &http.Server{
		Addr:              ":" + strconv.Itoa(port),
		Handler:           h,
		ReadHeaderTimeout: 5 * time.Second,
		IdleTimeout:       120 * time.Second,
		MaxHeaderBytes:    16 << 10,
	}
}
