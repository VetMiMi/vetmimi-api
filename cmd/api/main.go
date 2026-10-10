// Command api runs the VetMiMi HTTP API (default), its background worker
// (--mode worker), or enrols an administrator at a terminal (--mode
// create-user). Every mode shares one binary and one configuration;
// --mode healthcheck is the container health check (healthcheck.go).
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
	// The distroless image has no zoneinfo, and the practice timezone
	// (Australia/Sydney) must load anywhere.
	_ "time/tzdata"

	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/redis/go-redis/v9"

	"github.com/VetMiMi/vetmimi-api/internal/assistant"
	"github.com/VetMiMi/vetmimi-api/internal/auth"
	"github.com/VetMiMi/vetmimi-api/internal/config"
	"github.com/VetMiMi/vetmimi-api/internal/httpapi"
	"github.com/VetMiMi/vetmimi-api/internal/linkedin"
	"github.com/VetMiMi/vetmimi-api/internal/media"
	"github.com/VetMiMi/vetmimi-api/internal/meta"
	"github.com/VetMiMi/vetmimi-api/internal/postgres"
	"github.com/VetMiMi/vetmimi-api/internal/queue"
	"github.com/VetMiMi/vetmimi-api/internal/ratelimit"
	"github.com/VetMiMi/vetmimi-api/internal/video"
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

// run opens and migrates the database for every mode, so a failed migration
// exits non-zero before the api listens and the deploy health check sees a
// failed release.
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

// newLogger writes JSON lines to stdout, where the container runtime collects them.
func newLogger(level slog.Level) *slog.Logger {
	return slog.New(slog.NewJSONHandler(os.Stdout, &slog.HandlerOptions{Level: level}))
}

// openRedis returns a client without connecting: the api starts while Redis is
// down, and /readyz reports it. Errors never quote the URL: it may carry a password.
func openRedis(url string) (*redis.Client, error) {
	opts, err := redis.ParseURL(url)
	if err != nil {
		return nil, errors.New("open redis: the connection URL is invalid")
	}
	return redis.NewClient(opts), nil
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
