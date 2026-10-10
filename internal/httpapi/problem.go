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
	"strings"

	"github.com/jackc/pgx/v5/pgconn"

	"github.com/VetMiMi/vetmimi-api/internal/apperr"
	"github.com/VetMiMi/vetmimi-api/internal/httpapi/gen"
)

const problemTypePrefix = "urn:vetmimi:problem:"

// standardMembers are the Problem members an extension may not replace.
var standardMembers = map[string]bool{
	"type": true, "title": true, "status": true, "detail": true, "code": true, "errors": true,
}

// writeProblem answers with e as an RFC 9457 problem. Extensions become
// top-level members but never replace the standard ones.
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

// requestError answers a body the strict server could not decode. The
// decoder's message can quote the body, so only the error types are logged.
func requestError(log *slog.Logger) func(http.ResponseWriter, *http.Request, error) {
	return func(w http.ResponseWriter, r *http.Request, err error) {
		if e := bodyTooLarge(err); e != nil {
			writeProblem(w, e)
			return
		}
		rejected(log, w, r, "body", err)
	}
}

// paramError answers a parameter that failed to bind, naming it. The binder's
// message quotes the value, so only the error types are logged.
func paramError(log *slog.Logger) func(http.ResponseWriter, *http.Request, error) {
	return func(w http.ResponseWriter, r *http.Request, err error) {
		rejected(log, w, r, paramName(err), err)
	}
}

func rejected(log *slog.Logger, w http.ResponseWriter, r *http.Request, field string, err error) {
	log.InfoContext(r.Context(), "request rejected",
		"request_id", RequestID(r.Context()), "field", field, "err_types", errorTypes(err))
	writeProblem(w, apperr.Invalid("The request does not match the API contract.",
		apperr.FieldError{Field: field, Message: "is missing or malformed"}))
}

// errorTypes names each error type in err's chain: what went wrong, without
// the message that may quote the submitted value.
func errorTypes(err error) string {
	var types []string
	for ; err != nil; err = errors.Unwrap(err) {
		types = append(types, fmt.Sprintf("%T", err))
	}
	return strings.Join(types, " > ")
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

// responseError answers an error returned by a handler. Any untyped error is
// logged and answered with a fixed detail: its message may hold SQL,
// hostnames or personal data.
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
				"request_id", RequestID(r.Context()), "code", e.Code, "err", err)
		}
		writeProblem(w, e)
	}
}

// dependencyDown reports whether PostgreSQL, or the time to reach it, ran out.
func dependencyDown(err error) bool {
	var connect *pgconn.ConnectError
	return errors.Is(err, context.DeadlineExceeded) || errors.As(err, &connect)
}

// bodyTooLarge is the 413 for a body over its cap, or nil for any other error.
func bodyTooLarge(err error) *apperr.Error {
	var tooBig *http.MaxBytesError
	if !errors.As(err, &tooBig) {
		return nil
	}
	return apperr.New(apperr.PayloadTooLarge, fmt.Sprintf("The body is over its %d-byte limit.", tooBig.Limit))
}

func notFound(w http.ResponseWriter, _ *http.Request) {
	writeProblem(w, apperr.New(apperr.NotFound, "No route matches this method and path."))
}
