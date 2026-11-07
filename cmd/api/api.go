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
	"github.com/VetMiMi/vetmimi-api/internal/booking"
	"github.com/VetMiMi/vetmimi-api/internal/comms"
	"github.com/VetMiMi/vetmimi-api/internal/config"
	"github.com/VetMiMi/vetmimi-api/internal/content"
	"github.com/VetMiMi/vetmimi-api/internal/health"
	"github.com/VetMiMi/vetmimi-api/internal/linkedin"
	"github.com/VetMiMi/vetmimi-api/internal/media"
	"github.com/VetMiMi/vetmimi-api/internal/meta"
	"github.com/VetMiMi/vetmimi-api/internal/queue"
	"github.com/VetMiMi/vetmimi-api/internal/ratelimit"
	"github.com/VetMiMi/vetmimi-api/internal/settings"
	"github.com/VetMiMi/vetmimi-api/internal/signin"
	"github.com/VetMiMi/vetmimi-api/internal/video"
	"github.com/VetMiMi/vetmimi-api/internal/web"
)

// runAPI serves HTTP until ctx is done.
func runAPI(ctx context.Context, log *slog.Logger, cfg config.Config, pool *pgxpool.Pool, rdb *redis.Client) error {
	codes, err := auth.NewTOTP(cfg.TOTPEncryptionKey, time.Now)
	if err != nil {
		return err
	}
	lockout := auth.NewLockout(rdb, "", time.Now)
	sessions, err := auth.NewSessions(pool, codes, lockout, time.Now)
	if err != nil {
		return err
	}
	hub := video.NewHub(pool, log, time.Now)
	tasks := queue.New(rdb, log)

	r := web.NewRouter()
	limiter := web.NewLimiter(ratelimit.New(rdb, "", time.Now))
	routes := web.NewRoutes(r, cfg.ServiceKey, sessions, limiter)

	healthHandler := &health.Handler{
		PingPostgres: pool.Ping,
		PingRedis:    func(ctx context.Context) error { return rdb.Ping(ctx).Err() },
	}
	healthHandler.Routes(routes)

	signInHandler := &signin.Handler{
		Sessions: sessions,
	}
	signInHandler.Routes(routes)

	settingsHandler := &settings.Handler{
		Pool:  pool,
		Queue: tasks,
		Now:   time.Now,
	}
	settingsHandler.Routes(routes)

	bookingHandler := &booking.Handler{
		Pool:   pool,
		Secret: cfg.SigningSecret,
		Queue:  tasks,
		Hub:    hub,
		Now:    time.Now,
	}
	bookingHandler.Routes(routes)

	commsHandler := &comms.Handler{
		Pool:  pool,
		Queue: tasks,
		Now:   time.Now,
	}
	commsHandler.Routes(routes)

	videoHandler := &video.Handler{
		Pool: pool,
		Hub:  hub,
		Issuer: video.Issuer{
			Secret:       cfg.SigningSecret,
			PublicAPIURL: cfg.PublicAPIURL,
			TURNHost:     cfg.TURNHost,
			TURNSecret:   cfg.TURNSecret,
		},
		SiteURL: cfg.SiteURL,
		Now:     time.Now,
	}
	videoHandler.Routes(routes)

	mediaHandler := &media.Handler{
		Pool:      pool,
		Store:     media.NewStore(cfg),
		PublicURL: cfg.MediaPublicURL,
		Now:       time.Now,
	}
	mediaHandler.Routes(routes)

	contentHandler := &content.Handler{
		Pool:           pool,
		Queue:          tasks,
		MediaPublicURL: cfg.MediaPublicURL,
		Now:            time.Now,
	}
	contentHandler.Routes(routes)

	assistantHandler := &assistant.Handler{
		Pool:   pool,
		Client: assistant.New(cfg, log),
	}
	assistantHandler.Routes(routes)

	metaHandler := &meta.Handler{
		Connector: meta.New(cfg, pool, codes.Box, log),
	}
	metaHandler.Routes(routes)

	linkedinHandler := &linkedin.Handler{
		Connector: linkedin.New(cfg, pool, codes.Box, log),
	}
	linkedinHandler.Routes(routes)

	server := newServer(cfg.Port, r)
	if cfg.TURNHost == "" {
		// Development only: production refuses to start without TURN.
		log.Warn("turn_disabled", "detail", "TURN_HOST is empty; room tickets offer STUN only")
	}
	return serve(ctx, log, server, hub)
}

// serve runs server and the video hub until ctx is done, then shuts both down.
func serve(ctx context.Context, log *slog.Logger, server *http.Server, hub *video.Hub) error {
	hubDone := make(chan struct{})
	go func() {
		hub.Run(ctx)
		close(hubDone)
	}()
	served := make(chan error, 1)
	go func() {
		log.Info("api listening", "addr", server.Addr)
		served <- server.ListenAndServe()
	}()

	select {
	case err := <-served:
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
		return server.Shutdown(shutdown)
	}
}

// newServer sets no WriteTimeout: the video WebSocket stays open for a whole
// session, and every other route gets its deadline from the router.
func newServer(port int, handler http.Handler) *http.Server {
	return &http.Server{
		Addr:              ":" + strconv.Itoa(port),
		Handler:           handler,
		ReadHeaderTimeout: 5 * time.Second,
		IdleTimeout:       120 * time.Second,
		MaxHeaderBytes:    16 << 10,
	}
}
