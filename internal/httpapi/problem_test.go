package httpapi

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/stretchr/testify/require"

	"github.com/VetMiMi/vetmimi-api/internal/apperr"
	"github.com/VetMiMi/vetmimi-api/internal/httpapi/gen"
)

// failing answers /healthz with err, standing in for any handler whose
// domain call fails.
type failing struct {
	server
	err error
}

func (f *failing) GetHealthz(context.Context, gen.GetHealthzRequestObject) (gen.GetHealthzResponseObject, error) {
	return nil, f.err
}

// problemFrom asserts the response is a Problem and returns its members.
func problemFrom(t *testing.T, res *httptest.ResponseRecorder) map[string]any {
	t.Helper()
	require.Equal(t, "application/problem+json", res.Header().Get("Content-Type"))
	var body map[string]any
	require.NoError(t, json.Unmarshal(res.Body.Bytes(), &body))
	require.Equal(t, float64(res.Code), body["status"])
	require.Equal(t, "urn:vetmimi:problem:"+body["code"].(string), body["type"])
	return body
}

// failWith runs /healthz through a handler that returns err and captures the
// handler's own log, behind the request-id middleware.
func failWith(t *testing.T, err error) (*httptest.ResponseRecorder, map[string]any, string) {
	t.Helper()
	var logs bytes.Buffer
	log := slog.New(slog.NewJSONHandler(&logs, nil))
	r := chi.NewRouter()
	spec, specErr := gen.GetSpec()
	require.NoError(t, specErr)
	mountAPI(&failing{err: err}, spec, Deps{RateLimits: noRedis, Log: log})(r)
	h := requestID(r)

	res := httptest.NewRecorder()
	h.ServeHTTP(res, httptest.NewRequest(http.MethodGet, "/healthz", nil))
	return res, problemFrom(t, res), logs.String()
}

func TestEveryCodeIsAProblem(t *testing.T) {
	cases := []struct {
		code   apperr.Code
		status int
		title  string
	}{
		{apperr.InvalidRequest, 400, "Invalid request"},
		{apperr.Unauthenticated, 401, "Not signed in"},
		{apperr.InvalidCredentials, 401, "Invalid credentials"},
		{apperr.Forbidden, 403, "Forbidden"},
		{apperr.NotFound, 404, "Not found"},
		{apperr.SlotUnavailable, 409, "Time no longer available"},
		{apperr.StaleVersion, 409, "Changed by someone else"},
		{apperr.InvalidTransition, 409, "Status change not allowed"},
		{apperr.SlugTaken, 409, "Slug already in use"},
		{apperr.OverlappingPeriod, 409, "Overlapping availability period"},
		{apperr.InUse, 409, "Still in use"},
		{apperr.PayloadTooLarge, 413, "Payload too large"},
		{apperr.UnsupportedMediaType, 415, "Unsupported media type"},
		{apperr.OutsideBookingWindow, 422, "Outside the booking window"},
		{apperr.ServiceNotBookable, 422, "Service not bookable"},
		{apperr.BookingPaused, 422, "Booking paused"},
		{apperr.AcknowledgementRequired, 422, "Acknowledgement required"},
		{apperr.IdempotencyKeyReused, 422, "Idempotency key reused"},
		{apperr.ActionNotAllowed, 422, "Action not allowed"},
		{apperr.PublishRequirementsUnmet, 422, "Publishing requirements not met"},
		{apperr.RateLimited, 429, "Too many requests"},
		{apperr.InternalError, 500, "Internal error"},
		{apperr.Unavailable, 503, "Service unavailable"},
	}
	for _, c := range cases {
		t.Run(string(c.code), func(t *testing.T) {
			res := httptest.NewRecorder()
			writeProblem(res, apperr.New(c.code, "detail for "+string(c.code)))

			require.Equal(t, c.status, res.Code)
			body := problemFrom(t, res)
			require.Equal(t, "urn:vetmimi:problem:"+string(c.code), body["type"])
			require.Equal(t, c.title, body["title"])
			require.Equal(t, string(c.code), body["code"])
			require.Equal(t, "detail for "+string(c.code), body["detail"])
			require.NotContains(t, body, "errors")
		})
	}
}

func TestFieldErrorsAndExtensions(t *testing.T) {
	e := apperr.Invalid("two fields", apperr.FieldError{Field: "/email", Message: "required"})
	e.Extensions = map[string]any{"alternatives": []string{"slot"}, "code": "overridden", "detail": "overridden"}

	res := httptest.NewRecorder()
	writeProblem(res, e)
	body := problemFrom(t, res)

	require.Equal(t, []any{map[string]any{"field": "/email", "message": "required"}}, body["errors"])
	require.Equal(t, []any{"slot"}, body["alternatives"])
	require.Equal(t, "invalid_request", body["code"])
	require.Equal(t, "two fields", body["detail"])
}

func TestExtensionsCannotAddStandardMembers(t *testing.T) {
	e := apperr.New(apperr.NotFound, "")
	e.Extensions = map[string]any{"detail": "from an extension", "errors": "from an extension"}

	res := httptest.NewRecorder()
	writeProblem(res, e)
	body := problemFrom(t, res)
	require.NotContains(t, body, "detail")
	require.NotContains(t, body, "errors")
}

func TestTypedErrorFromAHandler(t *testing.T) {
	res, body, logs := failWith(t, fmt.Errorf("load service: %w", apperr.New(apperr.NotFound, "service 7")))
	require.Equal(t, http.StatusNotFound, res.Code)
	require.Equal(t, "service 7", body["detail"])
	require.Empty(t, logs, "a client error is not a server failure")
}

func TestUnexpectedErrorLeaksNothing(t *testing.T) {
	res, body, _ := failWith(t, errors.New("pq: secret detail"))
	require.Equal(t, http.StatusInternalServerError, res.Code)
	require.Equal(t, "internal_error", body["code"])
	require.Equal(t, "Unexpected failure.", body["detail"])
	require.NotContains(t, res.Body.String(), "secret")
}

func TestUnexpectedErrorIsLoggedWithTheRequestID(t *testing.T) {
	_, _, logs := failWith(t, errors.New("pq: secret detail"))
	lines := strings.Split(strings.TrimSpace(logs), "\n")
	require.Len(t, lines, 1)

	var line map[string]any
	require.NoError(t, json.Unmarshal([]byte(lines[0]), &line))
	require.Equal(t, "ERROR", line["level"])
	require.NotEmpty(t, line["request_id"])
	require.Equal(t, "pq: secret detail", line["err"])
}

func TestDeadlineExceededIs503(t *testing.T) {
	res, body, logs := failWith(t, fmt.Errorf("list slots: %w", context.DeadlineExceeded))
	require.Equal(t, http.StatusServiceUnavailable, res.Code)
	require.Equal(t, "unavailable", body["code"])
	require.Contains(t, logs, "request_id")
}

func TestPostgresConnectFailureIs503(t *testing.T) {
	// Port 1 refuses at once, so this is a real pgx connection failure
	// without a database.
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	_, err := pgconn.Connect(ctx, "postgres://nobody@127.0.0.1:1/none?sslmode=disable")
	var connect *pgconn.ConnectError
	require.ErrorAs(t, err, &connect)

	res, body, _ := failWith(t, fmt.Errorf("acquire: %w", err))
	require.Equal(t, http.StatusServiceUnavailable, res.Code)
	require.Equal(t, "unavailable", body["code"])
	require.NotContains(t, res.Body.String(), "127.0.0.1")
}

// decodeError reproduces what the strict server hands RequestErrorHandlerFunc
// when it cannot decode a JSON body.
func decodeError(body io.Reader) error {
	var v map[string]any
	if err := json.NewDecoder(body).Decode(&v); err != nil {
		return fmt.Errorf("can't decode JSON body: %w", err)
	}
	return nil
}

func TestMalformedJSONIs400(t *testing.T) {
	err := decodeError(strings.NewReader(`{"email":`))
	require.Error(t, err)

	res := httptest.NewRecorder()
	requestError(quiet)(res, httptest.NewRequest(http.MethodPost, "/", nil), err)
	require.Equal(t, http.StatusBadRequest, res.Code)
	require.Equal(t, "invalid_request", problemFrom(t, res)["code"])
}

func TestOversizedBodyIs413(t *testing.T) {
	body := http.MaxBytesReader(httptest.NewRecorder(), io.NopCloser(strings.NewReader(`{"note":"far too long"}`)), 8)
	err := decodeError(body)
	var tooBig *http.MaxBytesError
	require.ErrorAs(t, err, &tooBig)

	res := httptest.NewRecorder()
	requestError(quiet)(res, httptest.NewRequest(http.MethodPost, "/", nil), err)
	require.Equal(t, http.StatusRequestEntityTooLarge, res.Code)
	require.Equal(t, "payload_too_large", problemFrom(t, res)["code"])
}

// rejectedBy runs handle with a logger and answers the single field error,
// failing if the response or the log repeats the submitted value.
func rejectedBy(t *testing.T, handle func(*slog.Logger) func(http.ResponseWriter, *http.Request, error), err error) map[string]any {
	t.Helper()
	var logs bytes.Buffer
	res := httptest.NewRecorder()
	handle(slog.New(slog.NewJSONHandler(&logs, nil)))(res, httptest.NewRequest(http.MethodGet, "/", nil), err)

	require.Equal(t, http.StatusBadRequest, res.Code)
	body := problemFrom(t, res)
	require.Equal(t, "invalid_request", body["code"])
	require.NotContains(t, res.Body.String(), "secret-value-123")
	require.NotContains(t, logs.String(), "secret-value-123")
	require.Contains(t, logs.String(), `"err_types":"`)
	fields := body["errors"].([]any)
	require.Len(t, fields, 1)
	return fields[0].(map[string]any)
}

func TestParameterErrorNamesTheParameterWithoutTheValue(t *testing.T) {
	value := errors.New(`error unmarshaling 'secret-value-123' text as *types.UUID`)
	for _, err := range []error{
		&gen.InvalidParamFormatError{ParamName: "limit", Err: value},
		&gen.RequiredParamError{ParamName: "limit"},
		&gen.RequiredHeaderError{ParamName: "limit", Err: value},
		&gen.UnmarshalingParamError{ParamName: "limit", Err: value},
		&gen.TooManyValuesForParamError{ParamName: "limit", Count: 2},
		&gen.UnescapedCookieParamError{ParamName: "limit", Err: value},
	} {
		t.Run(fmt.Sprintf("%T", err), func(t *testing.T) {
			require.Equal(t, map[string]any{"field": "limit", "message": "is missing or malformed"},
				rejectedBy(t, paramError, err))
		})
	}
}

func TestUndecodableBodyIsNamedWithoutTheValue(t *testing.T) {
	err := fmt.Errorf("can't decode JSON body: %w", errors.New(`invalid value "secret-value-123"`))
	require.Equal(t, map[string]any{"field": "body", "message": "is missing or malformed"},
		rejectedBy(t, requestError, err))
}

func TestUnknownRouteIsAProblem(t *testing.T) {
	for _, method := range []string{http.MethodGet, http.MethodPost} {
		path := "/nope"
		if method == http.MethodPost {
			path = "/healthz" // a known path with the wrong method
		}
		res := httptest.NewRecorder()
		NewRouter(Deps{Log: quiet}).ServeHTTP(res, httptest.NewRequest(method, path, nil))
		require.Equal(t, http.StatusNotFound, res.Code, "%s %s", method, path)
		require.Equal(t, "not_found", problemFrom(t, res)["code"])
	}
}

func TestRateLimitedSetsRetryAfter(t *testing.T) {
	cases := map[time.Duration]string{
		1500 * time.Millisecond: "2",
		200 * time.Millisecond:  "1",
		0:                       "1",
	}
	for wait, want := range cases {
		res := httptest.NewRecorder()
		writeProblem(res, apperr.RateLimit(wait))
		require.Equal(t, http.StatusTooManyRequests, res.Code)
		require.Equalf(t, want, res.Header().Get("Retry-After"), "wait %s", wait)

		body := problemFrom(t, res)
		require.NotContains(t, body, "retry_after")
		require.NotContains(t, body, "RetryAfter")
	}
}

func TestOnlyRateLimitedSetsRetryAfter(t *testing.T) {
	res := httptest.NewRecorder()
	writeProblem(res, apperr.New(apperr.NotFound, ""))
	require.Empty(t, res.Header().Get("Retry-After"))
	require.NotContains(t, problemFrom(t, res), "detail")
}
