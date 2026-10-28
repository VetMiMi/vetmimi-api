package main

import (
	"context"
	"errors"
	"log/slog"
	"net/http"
	"strconv"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/redis/go-redis/v9"

	"github.com/VetMiMi/vetmimi-api/internal/assistant"
	"github.com/VetMiMi/vetmimi-api/internal/auth"
	"github.com/VetMiMi/vetmimi-api/internal/booking"
	"github.com/VetMiMi/vetmimi-api/internal/comms"
	"github.com/VetMiMi/vetmimi-api/internal/config"
	"github.com/VetMiMi/vetmimi-api/internal/content"
	"github.com/VetMiMi/vetmimi-api/internal/httpapi"
	"github.com/VetMiMi/vetmimi-api/internal/linkedin"
	"github.com/VetMiMi/vetmimi-api/internal/media"
	"github.com/VetMiMi/vetmimi-api/internal/meta"
	"github.com/VetMiMi/vetmimi-api/internal/queue"
	"github.com/VetMiMi/vetmimi-api/internal/ratelimit"
	"github.com/VetMiMi/vetmimi-api/internal/settings"
	"github.com/VetMiMi/vetmimi-api/internal/video"
	"github.com/VetMiMi/vetmimi-api/internal/web"
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
	tasks := queue.New(rdb, log)
	counter := ratelimit.New(rdb, "", time.Now)
	store := media.NewStore(cfg)
	ai := assistant.New(cfg, log)
	metaConnector := meta.New(cfg, pool, codes.Box, log)
	linkedInConnector := linkedin.New(cfg, pool, codes.Box, log)

	r := web.NewRouter()
	routes := web.NewRoutes(r, cfg.ServiceKey, sessions, web.NewLimiter(counter))
	pingRedis := func(ctx context.Context) error { return rdb.Ping(ctx).Err() }
	r.GET("/healthz", healthz)
	r.GET("/readyz", readyz(pool.Ping, pingRedis))
	(&authHandler{Sessions: sessions}).Routes(routes)
	(&settings.Handler{Pool: pool, Queue: tasks, Now: time.Now}).Routes(routes)
	(&booking.Handler{Pool: pool, Secret: cfg.SigningSecret, Queue: tasks, Hub: hub, Now: time.Now}).Routes(routes)
	(&comms.Handler{Pool: pool, Queue: tasks, Now: time.Now}).Routes(routes)
	(&video.Handler{Pool: pool, Hub: hub, SiteURL: cfg.SiteURL, Now: time.Now, Issuer: video.Issuer{
		Secret: cfg.SigningSecret, PublicAPIURL: cfg.PublicAPIURL, TURNHost: cfg.TURNHost, TURNSecret: cfg.TURNSecret,
	}}).Routes(routes)
	(&media.Handler{Pool: pool, Store: store, PublicURL: cfg.MediaPublicURL, Now: time.Now}).Routes(routes)
	(&content.Handler{Pool: pool, Queue: tasks, MediaPublicURL: cfg.MediaPublicURL, Now: time.Now}).Routes(routes)
	(&assistant.Handler{Pool: pool, Client: ai}).Routes(routes)
	(&meta.Handler{Connector: metaConnector, Now: time.Now}).Routes(routes)
	(&linkedin.Handler{Connector: linkedInConnector, Now: time.Now}).Routes(routes)

	// Routes not yet moved to Gin fall through to the generated router.
	r.NoRoute(gin.WrapH(httpapi.NewRouter(httpapi.Deps{
		PingPostgres:   pool.Ping,
		PingRedis:      pingRedis,
		Log:            log,
		ServiceKey:     cfg.ServiceKey,
		RateLimits:     httpapi.NewRateLimits(counter, log, time.Now),
		Sessions:       sessions,
		Pool:           pool,
		Queue:          tasks,
		SigningSecret:  cfg.SigningSecret,
		PublicAPIURL:   cfg.PublicAPIURL,
		TURNHost:       cfg.TURNHost,
		TURNSecret:     cfg.TURNSecret,
		SiteURL:        cfg.SiteURL,
		Hub:            hub,
		Media:          store,
		MediaPublicURL: cfg.MediaPublicURL,
		Meta:           metaConnector,
		LinkedIn:       linkedInConnector,
		Assistant:      ai,
		Now:            time.Now,
	})))
	srv := newServer(cfg.Port, r)
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
