package main

import (
	"context"
	"net/http"
	"time"

	"github.com/gin-gonic/gin"
)

func healthz(c *gin.Context) {
	c.JSON(http.StatusOK, gin.H{"status": "ok"})
}

type readiness struct {
	Status string `json:"status"`
	Checks struct {
		Postgres string `json:"postgres"`
		Redis    string `json:"redis"`
	} `json:"checks"`
}

// readyz answers 503 while PostgreSQL or Redis is unreachable, so the host
// keeps traffic away from a half-started process.
func readyz(pingPostgres, pingRedis func(context.Context) error) gin.HandlerFunc {
	return func(c *gin.Context) {
		var r readiness
		r.Status, r.Checks.Postgres, r.Checks.Redis = "ok", "ok", "ok"
		if ping(c.Request.Context(), pingPostgres) != nil {
			r.Status, r.Checks.Postgres = "unavailable", "fail"
		}
		if ping(c.Request.Context(), pingRedis) != nil {
			r.Status, r.Checks.Redis = "unavailable", "fail"
		}
		status := http.StatusOK
		if r.Status != "ok" {
			status = http.StatusServiceUnavailable
		}
		c.JSON(status, r)
	}
}

func ping(ctx context.Context, fn func(context.Context) error) error {
	ctx, cancel := context.WithTimeout(ctx, time.Second)
	defer cancel()
	return fn(ctx)
}
