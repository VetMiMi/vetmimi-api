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

type fieldResponse struct {
	Field   string `json:"field"`
	Message string `json:"message"`
}

// Error answers err as a problem. Untyped errors get a fixed message: theirs may hold SQL or personal data.
func Error(c *gin.Context, err error) {
	var problem *apperr.Error
	switch {
	case errors.As(err, &problem):
	case dependencyDown(err):
		problem = apperr.New(apperr.Unavailable, "A dependency is unreachable; try again shortly.")
	default:
		problem = apperr.New(apperr.InternalError, "Unexpected failure.")
	}
	if problem.Code.Status() >= 500 {
		slog.ErrorContext(c.Request.Context(), "request failed",
			"request_id", RequestID(c.Request.Context()),
			"code", problem.Code,
			"err", err)
	}
	abort(c, problem)
}

func dependencyDown(err error) bool {
	var connect *pgconn.ConnectError
	return errors.Is(err, context.DeadlineExceeded) || errors.As(err, &connect)
}

// abort writes problem as an RFC 9457 problem, with its extensions (such as alternatives) beside the standard members.
func abort(c *gin.Context, problem *apperr.Error) {
	body := gin.H{}
	for key, value := range problem.Extensions {
		body[key] = value
	}
	body["type"] = "urn:vetmimi:problem:" + string(problem.Code)
	body["title"] = problem.Code.Title()
	body["status"] = problem.Code.Status()
	body["code"] = problem.Code
	if problem.Detail != "" {
		body["detail"] = problem.Detail
	}
	if len(problem.Fields) > 0 {
		fields := make([]fieldResponse, len(problem.Fields))
		for i, field := range problem.Fields {
			fields[i] = fieldResponse{Field: field.Field, Message: field.Message}
		}
		body["errors"] = fields
	}
	if problem.Code == apperr.RateLimited {
		// Rounded up and never 0: a client that retries early is refused again.
		seconds := max(1, int(math.Ceil(problem.RetryAfter.Seconds())))
		c.Header("Retry-After", strconv.Itoa(seconds))
	}
	c.Header("Content-Type", "application/problem+json")
	c.AbortWithStatusJSON(problem.Code.Status(), body)
}
