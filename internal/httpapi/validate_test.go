package httpapi

import (
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/getkin/kin-openapi/openapi3"
	"github.com/go-chi/chi/v5"
	"github.com/stretchr/testify/require"

	"github.com/VetMiMi/vetmimi-api/internal/httpapi/gen"
)

const (
	serviceID  = "0192f1c4-7b3a-7d4e-9f10-2a3b4c5d6e7f"
	idemKey    = "0192f1c4-7b3a-7d4e-9f10-2a3b4c5d6e80"
	validBody  = `{"email": "mi@example.com", "startsAt": "2026-10-05T10:00:00+11:00", "mode": "video"}`
	bookingURL = "/bookings/" + serviceID + "?date=2026-10-05"
)

func fixture(t *testing.T) *openapi3.T {
	t.Helper()
	spec, err := openapi3.NewLoader().LoadFromFile("testdata/validate.yaml")
	require.NoError(t, err)
	return spec
}

// booking sends a request to the fixture's body operation through the real
// middleware chain, after edit has changed whatever the test is about. The
// handler answers with the body it received.
func booking(t *testing.T, body string, edit func(*http.Request)) *httptest.ResponseRecorder {
	t.Helper()
	req := httptest.NewRequest(http.MethodPost, bookingURL, strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("X-Service-Key", "key")
	req.Header.Set("Idempotency-Key", idemKey)
	if edit != nil {
		edit(req)
	}
	validate := validateRequests(fixture(t))
	res, _ := through(t, func(r chi.Router) {
		r.With(validate).Post("/bookings/{serviceId}", func(w http.ResponseWriter, r *http.Request) {
			_, _ = io.Copy(w, r.Body)
		})
	}, req)
	return res
}

// invalid asserts res is a 400 invalid_request and returns its errors[].
func invalid(t *testing.T, res *httptest.ResponseRecorder) []map[string]any {
	t.Helper()
	require.Equal(t, http.StatusBadRequest, res.Code, res.Body.String())
	p := problemFrom(t, res)
	require.Equal(t, "invalid_request", p["code"])
	var errs []map[string]any
	for _, e := range p["errors"].([]any) {
		errs = append(errs, e.(map[string]any))
	}
	return errs
}

func fieldError(field, message string) map[string]any {
	return map[string]any{"field": field, "message": message}
}

func TestValidBodyReachesTheHandlerUnchanged(t *testing.T) {
	res := booking(t, validBody, nil)
	require.Equal(t, http.StatusOK, res.Code, res.Body.String())
	require.Equal(t, validBody, res.Body.String(), "no default filled in, nothing re-encoded")
}

func TestMissingRequiredFieldIsNamedByPointer(t *testing.T) {
	res := booking(t, `{"startsAt": "2026-10-05T10:00:00Z", "mode": "video"}`, nil)
	errs := invalid(t, res)
	require.Equal(t, "/email", errs[0]["field"])
	require.Equal(t, "is required", errs[0]["message"])
}

func TestBrokenRulesAreAnsweredWithoutTheValue(t *testing.T) {
	for name, tc := range map[string]struct{ body, field, message, secret string }{
		"enum": {
			`{"email": "mi@example.com", "startsAt": "2026-10-05T10:00:00Z", "mode": "zz-secret-zz"}`,
			"/mode", "must be one of: video, in_person", "zz-secret-zz",
		},
		"format": {
			`{"email": "zz-secret-zz", "startsAt": "2026-10-05T10:00:00Z", "mode": "video"}`,
			"/email", "must be a valid email", "zz-secret-zz",
		},
		"pattern": {
			`{"email": "mi@example.com", "startsAt": "2026-10-05T10:00:00Z", "mode": "video", "slug": "ZZ Secret ZZ"}`,
			"/slug", "must match the pattern ^[a-z0-9]+(-[a-z0-9]+)*$", "ZZ Secret ZZ",
		},
		"type": {
			`{"email": "mi@example.com", "startsAt": "2026-10-05T10:00:00Z", "mode": "video", "guests": "zz-secret-zz"}`,
			"/guests", "must be of type integer", "zz-secret-zz",
		},
		"maximum": {
			`{"email": "mi@example.com", "startsAt": "2026-10-05T10:00:00Z", "mode": "video", "guests": 987654}`,
			"/guests", "must be at most 4", "987654",
		},
		"minimum": {
			`{"email": "mi@example.com", "startsAt": "2026-10-05T10:00:00Z", "mode": "video", "guests": -987654}`,
			"/guests", "must be at least 1", "987654",
		},
		"minLength": {
			`{"email": "mi@example.com", "startsAt": "2026-10-05T10:00:00Z", "mode": "video", "tags": ["Z"]}`,
			"/tags/0", "must be at least 2 characters", `"Z"`,
		},
		"minItems": {
			`{"email": "mi@example.com", "startsAt": "2026-10-05T10:00:00Z", "mode": "video", "tags": []}`,
			"/tags", "must have at least 1 items", "[]",
		},
		"maxItems": {
			`{"email": "mi@example.com", "startsAt": "2026-10-05T10:00:00Z", "mode": "video", "tags": ["zz-secret-zz", "bb", "cc"]}`,
			"/tags", "must have at most 2 items", "zz-secret-zz",
		},
		"uniqueItems": {
			`{"email": "mi@example.com", "startsAt": "2026-10-05T10:00:00Z", "mode": "video", "tags": ["zz-secret-zz", "zz-secret-zz"]}`,
			"/tags", "must not repeat an item", "zz-secret-zz",
		},
		"nested maxLength": {
			`{"email": "mi@example.com", "startsAt": "2026-10-05T10:00:00Z", "mode": "video", "contact": {"phone": "zz-secret-zz-0412345678"}}`,
			"/contact/phone", "must be at most 20 characters", "zz-secret-zz",
		},
	} {
		t.Run(name, func(t *testing.T) {
			res := booking(t, tc.body, nil)
			require.Equal(t, []map[string]any{fieldError(tc.field, tc.message)}, invalid(t, res))
			require.NotContains(t, res.Body.String(), tc.secret)
		})
	}

	t.Run("type of a parameter", func(t *testing.T) {
		res := booking(t, validBody, func(r *http.Request) { r.URL.RawQuery = "limit=zz-secret-zz" })
		require.Equal(t, []map[string]any{fieldError("limit", "must be of type integer")}, invalid(t, res))
		require.NotContains(t, res.Body.String(), "zz-secret-zz")
	})
}

func TestStringFormatsAreChecked(t *testing.T) {
	for name, tc := range map[string]struct {
		body           string
		edit           func(*http.Request)
		field, message string
	}{
		"uuid in the path": {validBody, func(r *http.Request) {
			r.URL.Path = "/bookings/not-a-uuid"
		}, "serviceId", "must be a valid uuid"},
		"uuid in a header": {validBody, func(r *http.Request) {
			r.Header.Set("Idempotency-Key", "0192f1c4-7b3a-7d4e-9f10")
		}, "Idempotency-Key", "must be a valid uuid"},
		// The 31st of February matches kin-openapi's own date patterns.
		"date in the query": {validBody, func(r *http.Request) {
			r.URL.RawQuery = "date=2026-02-31"
		}, "date", "must be a valid date"},
		"date-time in the body": {
			`{"email": "mi@example.com", "startsAt": "2026-02-31T10:00:00Z", "mode": "video"}`,
			nil, "/startsAt", "must be a valid date-time",
		},
		"email in the body": {
			`{"email": "Daw Mi <mi@example.com>", "startsAt": "2026-10-05T10:00:00Z", "mode": "video"}`,
			nil, "/email", "must be a valid email",
		},
	} {
		t.Run(name, func(t *testing.T) {
			res := booking(t, tc.body, tc.edit)
			require.Equal(t, []map[string]any{fieldError(tc.field, tc.message)}, invalid(t, res))
		})
	}
}

func TestUnknownPropertiesAreRejected(t *testing.T) {
	res := booking(t, `{"email": "mi@example.com", "startsAt": "2026-10-05T10:00:00Z", "mode": "video",
		"nickname": "zz-secret-zz", "a/b~c": 1, "contact": {"fax": "zz-secret-zz"}}`, nil)
	require.Equal(t, []map[string]any{
		fieldError("/a~1b~0c", "is not an allowed property"),
		fieldError("/contact/fax", "is not an allowed property"),
		fieldError("/nickname", "is not an allowed property"),
	}, invalid(t, res))
	require.NotContains(t, res.Body.String(), "zz-secret-zz")
}

func TestMissingBodyIsRejected(t *testing.T) {
	require.Equal(t, []map[string]any{fieldError("", "is required")}, invalid(t, booking(t, "", nil)))
}

func TestBodyThatIsNotJSONIsRejected(t *testing.T) {
	res := booking(t, `zz-secret-zz`, nil)
	require.Equal(t, []map[string]any{
		fieldError("", "must be a well-formed body for its Content-Type"),
	}, invalid(t, res))
	require.NotContains(t, res.Body.String(), "zz-secret-zz")
}

func TestUnacceptedContentTypeIsRejected(t *testing.T) {
	res := booking(t, validBody, func(r *http.Request) { r.Header.Set("Content-Type", "text/zz-secret-zz") })
	require.Equal(t, []map[string]any{
		fieldError("Content-Type", "must be application/json"),
	}, invalid(t, res))
	require.NotContains(t, res.Body.String(), "zz-secret-zz")
}

func TestAllProblemsComeBackTogether(t *testing.T) {
	res := booking(t, `{"startsAt": "2026-10-05T10:00:00Z", "mode": "boat"}`, func(r *http.Request) {
		r.URL.Path = "/bookings/not-a-uuid"
		r.URL.RawQuery = "date=tomorrow"
		r.Header.Del("Idempotency-Key")
	})
	require.Equal(t, []map[string]any{
		fieldError("/email", "is required"),
		fieldError("/mode", "must be one of: video, in_person"),
		fieldError("Idempotency-Key", "is required"),
		fieldError("date", "must be a valid date"),
		fieldError("serviceId", "must be a valid uuid"),
	}, invalid(t, res))
}

func TestBodyOverTheCapIs413NotA400(t *testing.T) {
	res := booking(t, `{"email": "`+strings.Repeat("a", defaultBodyCap)+`"}`, nil)
	require.Equal(t, http.StatusRequestEntityTooLarge, res.Code, res.Body.String())
	require.Equal(t, "payload_too_large", problemFrom(t, res)["code"])
}

func TestValidatorIgnoresServerHost(t *testing.T) {
	req := httptest.NewRequest(http.MethodGet, "/healthz", nil)
	req.Host = "api.vetmimi.example"
	res := httptest.NewRecorder()
	NewRouter(Deps{Log: quiet}).ServeHTTP(res, req)
	require.Equal(t, http.StatusOK, res.Code, res.Body.String())
}

func TestGeneratedRoutesAreValidated(t *testing.T) {
	mount := mountAPI(&server{}, fixture(t), "", quiet)

	res, _ := through(t, mount, httptest.NewRequest(http.MethodGet, "/healthz", nil))
	require.Equal(t, []map[string]any{fieldError("probe", "is required")}, invalid(t, res))

	res, _ = through(t, mount, httptest.NewRequest(http.MethodGet, "/healthz?probe=yes", nil))
	require.Equal(t, http.StatusOK, res.Code, res.Body.String())
}

// The generated wrapper decides the order of ChiServerOptions.Middlewares,
// and mountAPI relies on it to run validation after everything else.
func TestOperationMiddlewaresRunLastEntryFirst(t *testing.T) {
	var ran []string
	mark := func(name string) gen.MiddlewareFunc {
		return func(next http.Handler) http.Handler {
			return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				ran = append(ran, name)
				next.ServeHTTP(w, r)
			})
		}
	}
	h := gen.HandlerWithOptions(gen.NewStrictHandler(&server{}, nil), gen.ChiServerOptions{
		Middlewares: []gen.MiddlewareFunc{mark("first entry"), mark("second entry")},
	})

	h.ServeHTTP(httptest.NewRecorder(), httptest.NewRequest(http.MethodGet, "/healthz", nil))
	require.Equal(t, []string{"second entry", "first entry"}, ran)
}
