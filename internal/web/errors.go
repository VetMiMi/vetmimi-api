package web

import (
	"context"
	"errors"
	"log/slog"
	"math"
	"strconv"

	"github.com/gin-gonic/gin"
	"github.com/jackc/pgx/v5/pgconn"

	"github.com/VetMiMi/vetmimi-api/internal/apperr"
)

// Error answers err as a problem. Untyped errors get a fixed message: theirs may hold SQL or personal data.
func Error(c *gin.Context, err error) {
	var e *apperr.Error
	switch {
	case errors.As(err, &e):
	case dependencyDown(err):
		e = apperr.New(apperr.Unavailable, "A dependency is unreachable; try again shortly.")
	default:
		e = apperr.New(apperr.InternalError, "Unexpected failure.")
	}
	if e.Code.Status() >= 500 {
		slog.ErrorContext(c.Request.Context(), "request failed",
			"request_id", RequestID(c.Request.Context()), "code", e.Code, "err", err)
	}
	abort(c, e)
}

func dependencyDown(err error) bool {
	var connect *pgconn.ConnectError
	return errors.Is(err, context.DeadlineExceeded) || errors.As(err, &connect)
}

type fieldJSON struct {
	Field   string `json:"field"`
	Message string `json:"message"`
}

// abort writes e as an RFC 9457 problem, with its extensions (such as alternatives) beside the standard members.
func abort(c *gin.Context, e *apperr.Error) {
	body := gin.H{}
	for k, v := range e.Extensions {
		body[k] = v
	}
	body["type"] = "urn:vetmimi:problem:" + string(e.Code)
	body["title"] = e.Code.Title()
	body["status"] = e.Code.Status()
	body["code"] = e.Code
	if e.Detail != "" {
		body["detail"] = e.Detail
	}
	if len(e.Fields) > 0 {
		fields := make([]fieldJSON, len(e.Fields))
		for i, f := range e.Fields {
			fields[i] = fieldJSON{f.Field, f.Message}
		}
		body["errors"] = fields
	}
	if e.Code == apperr.RateLimited {
		// Rounded up and never 0: a client that retries early is refused again.
		c.Header("Retry-After", strconv.Itoa(max(1, int(math.Ceil(e.RetryAfter.Seconds())))))
	}
	c.Header("Content-Type", "application/problem+json")
	c.AbortWithStatusJSON(e.Code.Status(), body)
}
