package httpapi

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"math"
	"net/http"
	"strconv"

	"github.com/go-chi/chi/v5/middleware"
	"github.com/jackc/pgx/v5/pgconn"

	"github.com/VetMiMi/vetmimi-api/internal/httpapi/gen"
	"github.com/VetMiMi/vetmimi-api/internal/platform/apperr"
)

const problemTypePrefix = "urn:vetmimi:problem:"

// standardMembers are the Problem members an extension may not replace.
var standardMembers = map[string]bool{
	"type": true, "title": true, "status": true, "detail": true, "code": true, "errors": true,
}

// writeProblem answers with e as an RFC 9457 problem. Extensions become
// top-level members, but never replace the standard ones.
func writeProblem(w http.ResponseWriter, e *apperr.Error) {
	body := make(map[string]any, len(e.Extensions)+len(standardMembers))
	for k, v := range e.Extensions {
		if !standardMembers[k] {
			body[k] = v
		}
	}
	status := e.Code.Status()
	body["type"] = problemTypePrefix + string(e.Code)
	body["title"] = e.Code.Title()
	body["status"] = status
	body["code"] = e.Code
	if e.Detail != "" {
		body["detail"] = e.Detail
	}
	if len(e.Fields) > 0 {
		fields := make([]map[string]string, len(e.Fields))
		for i, f := range e.Fields {
			fields[i] = map[string]string{"field": f.Field, "message": f.Message}
		}
		body["errors"] = fields
	}

	w.Header().Set("Content-Type", "application/problem+json")
	if e.Code == apperr.RateLimited {
		w.Header().Set("Retry-After", strconv.Itoa(retryAfterSeconds(e)))
	}
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(body)
}

// retryAfterSeconds rounds up, because a client that retries early is refused
// again, and never says 0, which would invite an immediate retry.
func retryAfterSeconds(e *apperr.Error) int {
	return max(1, int(math.Ceil(e.RetryAfter.Seconds())))
}

// requestError answers a body the strict server could not decode.
func requestError(w http.ResponseWriter, _ *http.Request, err error) {
	var tooBig *http.MaxBytesError
	if errors.As(err, &tooBig) {
		writeProblem(w, apperr.New(apperr.PayloadTooLarge,
			fmt.Sprintf("The body is over its %d-byte limit.", tooBig.Limit)))
		return
	}
	writeProblem(w, apperr.New(apperr.InvalidRequest, err.Error()))
}

// paramError answers a path, query or header parameter that failed to bind,
// naming the parameter so the caller can find it.
func paramError(w http.ResponseWriter, _ *http.Request, err error) {
	writeProblem(w, apperr.Invalid("A parameter is missing or malformed.",
		apperr.FieldError{Field: paramName(err), Message: err.Error()}))
}

func paramName(err error) string {
	var (
		format    *gen.InvalidParamFormatError
		required  *gen.RequiredParamError
		header    *gen.RequiredHeaderError
		unmarshal *gen.UnmarshalingParamError
		tooMany   *gen.TooManyValuesForParamError
		cookie    *gen.UnescapedCookieParamError
	)
	switch {
	case errors.As(err, &format):
		return format.ParamName
	case errors.As(err, &required):
		return required.ParamName
	case errors.As(err, &header):
		return header.ParamName
	case errors.As(err, &unmarshal):
		return unmarshal.ParamName
	case errors.As(err, &tooMany):
		return tooMany.ParamName
	case errors.As(err, &cookie):
		return cookie.ParamName
	}
	return ""
}

// responseError answers an error returned by a handler. Typed errors go out
// as they are; anything else is logged with the request id and answered with
// a fixed detail, because its message may hold SQL, hostnames or personal data.
func responseError(log *slog.Logger) func(http.ResponseWriter, *http.Request, error) {
	return func(w http.ResponseWriter, r *http.Request, err error) {
		var e *apperr.Error
		switch {
		case errors.As(err, &e):
		case dependencyDown(err):
			e = apperr.New(apperr.Unavailable, "A dependency is unreachable; try again shortly.")
		default:
			e = apperr.New(apperr.InternalError, "Unexpected failure.")
		}
		if e.Code.Status() >= http.StatusInternalServerError {
			log.ErrorContext(r.Context(), "request failed",
				"request_id", middleware.GetReqID(r.Context()), "code", e.Code, "err", err)
		}
		writeProblem(w, e)
	}
}

// dependencyDown reports whether err means PostgreSQL (or the time budget for
// reaching it) ran out, rather than a bug.
func dependencyDown(err error) bool {
	var connect *pgconn.ConnectError
	return errors.Is(err, context.DeadlineExceeded) || errors.As(err, &connect)
}

func notFound(w http.ResponseWriter, _ *http.Request) {
	writeProblem(w, apperr.New(apperr.NotFound, "No route matches this method and path."))
}
