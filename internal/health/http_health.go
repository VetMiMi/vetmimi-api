package health

import (
	"context"
	"net/http"
	"time"

	"github.com/gin-gonic/gin"
)

type healthResponse struct {
	Status string `json:"status"`
}

type readyResponse struct {
	Status string         `json:"status"`
	Checks checksResponse `json:"checks"`
}

type checksResponse struct {
	Postgres string `json:"postgres"`
	Redis    string `json:"redis"`
}

func (h *Handler) healthz(c *gin.Context) {
	c.JSON(http.StatusOK, healthResponse{Status: "ok"})
}

// readyz answers 503 while PostgreSQL or Redis is unreachable, so the host
// keeps traffic away from a half-started process.
func (h *Handler) readyz(c *gin.Context) {
	ctx := c.Request.Context()

	postgresStatus := check(ctx, h.PingPostgres)
	redisStatus := check(ctx, h.PingRedis)

	response := readyResponse{
		Status: "ok",
		Checks: checksResponse{
			Postgres: postgresStatus,
			Redis:    redisStatus,
		},
	}
	status := http.StatusOK
	if postgresStatus != "ok" || redisStatus != "ok" {
		response.Status = "unavailable"
		status = http.StatusServiceUnavailable
	}
	c.JSON(status, response)
}

// check gives each dependency one second, so a hung one cannot hold the probe.
func check(ctx context.Context, ping func(context.Context) error) string {
	ctx, cancel := context.WithTimeout(ctx, time.Second)
	defer cancel()
	err := ping(ctx)
	if err != nil {
		return "fail"
	}
	return "ok"
}
