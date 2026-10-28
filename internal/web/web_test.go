package web_test

import (
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/require"

	"github.com/VetMiMi/vetmimi-api/internal/apperr"
	"github.com/VetMiMi/vetmimi-api/internal/ratelimit"
	"github.com/VetMiMi/vetmimi-api/internal/redistest"
	"github.com/VetMiMi/vetmimi-api/internal/web"
)

func send(r http.Handler, method, path, body string, headers ...string) *httptest.ResponseRecorder {
	req := httptest.NewRequest(method, path, strings.NewReader(body))
	for i := 0; i+1 < len(headers); i += 2 {
		req.Header.Set(headers[i], headers[i+1])
	}
	res := httptest.NewRecorder()
	r.ServeHTTP(res, req)
	return res
}

func problem(t *testing.T, res *httptest.ResponseRecorder, status int, code string) map[string]any {
	t.Helper()
	require.Equal(t, status, res.Code, res.Body.String())
	require.Equal(t, "application/problem+json", res.Header().Get("Content-Type"))
	var p map[string]any
	require.NoError(t, json.Unmarshal(res.Body.Bytes(), &p))
	require.Equal(t, code, p["code"])
	require.Equal(t, "urn:vetmimi:problem:"+code, p["type"])
	return p
}

type input struct {
	Name    string `json:"name" binding:"required,max=5"`
	Periods []struct {
		Start string `json:"start" binding:"required,hhmm"`
	} `json:"periods" binding:"max=2,dive"`
}

func TestBindJSONNamesEachFieldAsAPointer(t *testing.T) {
	r := web.NewRouter()
	r.POST("/things", web.MaxBody(100), func(c *gin.Context) {
		var in input
		if web.BindJSON(c, &in) {
			c.JSON(http.StatusOK, in)
		}
	})

	require.Equal(t, http.StatusOK, send(r, http.MethodPost, "/things", `{"name": "Mi"}`).Code)

	p := problem(t, send(r, http.MethodPost, "/things", `{"name": "Daw Mi Mi", "periods": [{"start": "25:00"}]}`),
		http.StatusBadRequest, "invalid_request")
	require.Equal(t, []any{
		map[string]any{"field": "/name", "message": "must be at most 5 characters"},
		map[string]any{"field": "/periods/0/start", "message": "must be a time as HH:MM"},
	}, p["errors"])

	p = problem(t, send(r, http.MethodPost, "/things", `{"name": 7}`), http.StatusBadRequest, "invalid_request")
	require.Equal(t, []any{map[string]any{"field": "/name", "message": "must be of type string"}}, p["errors"])

	p = problem(t, send(r, http.MethodPost, "/things", `{"name": "Mi", "extra": 1}`), http.StatusBadRequest, "invalid_request")
	require.Equal(t, []any{map[string]any{"field": "/extra", "message": "is not an allowed property"}}, p["errors"])
	p = problem(t, send(r, http.MethodPost, "/things", `{"name": "Mi", "periods": [{"start": "09:00", "end": "10:00"}]}`),
		http.StatusBadRequest, "invalid_request")
	require.Equal(t, []any{map[string]any{"field": "/periods/0/end", "message": "is not an allowed property"}}, p["errors"])

	problem(t, send(r, http.MethodPost, "/things", `{"name": "`+strings.Repeat("x", 200)+`"}`),
		http.StatusRequestEntityTooLarge, "payload_too_large")
}

func TestBindNamesAValueThatFailsToDecode(t *testing.T) {
	r := web.NewRouter()
	r.POST("/things", func(c *gin.Context) {
		var in struct {
			At    time.Time `json:"at" binding:"required"`
			Inner *struct {
				Times []time.Time `json:"times"`
			} `json:"inner"`
		}
		if web.BindJSON(c, &in) {
			c.Status(http.StatusOK)
		}
	})
	r.GET("/things", func(c *gin.Context) {
		var q struct {
			Limit *int      `form:"limit"`
			From  time.Time `form:"from" time_format:"2006-01-02"`
		}
		if web.BindQuery(c, &q) {
			c.Status(http.StatusOK)
		}
	})

	p := problem(t, send(r, http.MethodPost, "/things", `{"at": "tomorrow"}`), http.StatusBadRequest, "invalid_request")
	require.Equal(t, []any{map[string]any{"field": "/at", "message": "must be a valid date-time"}}, p["errors"])
	p = problem(t, send(r, http.MethodPost, "/things", `{"at": "2026-10-28T09:00:00Z", "inner": {"times": ["soon"]}}`),
		http.StatusBadRequest, "invalid_request")
	require.Equal(t, []any{map[string]any{"field": "/inner/times", "message": "must be a valid date-time"}}, p["errors"])

	p = problem(t, send(r, http.MethodGet, "/things?limit=abc", ""), http.StatusBadRequest, "invalid_request")
	require.Equal(t, []any{map[string]any{"field": "limit", "message": "must be of type integer"}}, p["errors"])
	p = problem(t, send(r, http.MethodGet, "/things?from=monday", ""), http.StatusBadRequest, "invalid_request")
	require.Equal(t, []any{map[string]any{"field": "from", "message": "must be a valid date"}}, p["errors"])
}

func TestErrorHidesUntypedErrors(t *testing.T) {
	r := web.NewRouter()
	r.GET("/typed", func(c *gin.Context) {
		web.Error(c, &apperr.Error{Code: apperr.SlotUnavailable, Detail: "Taken.",
			Extensions: map[string]any{"alternatives": []string{}}})
	})
	r.GET("/untyped", func(c *gin.Context) { web.Error(c, errors.New("SELECT secret FROM users")) })
	r.GET("/panic", func(c *gin.Context) { panic("boom") })

	p := problem(t, send(r, http.MethodGet, "/typed", ""), http.StatusConflict, "slot_unavailable")
	require.Equal(t, "Taken.", p["detail"])
	require.Equal(t, []any{}, p["alternatives"])

	res := send(r, http.MethodGet, "/untyped", "")
	p = problem(t, res, http.StatusInternalServerError, "internal_error")
	require.NotContains(t, res.Body.String(), "secret")
	require.Equal(t, "Unexpected failure.", p["detail"])

	problem(t, send(r, http.MethodGet, "/panic", ""), http.StatusInternalServerError, "internal_error")
	problem(t, send(r, http.MethodGet, "/nowhere", ""), http.StatusNotFound, "not_found")
}

func TestServiceKey(t *testing.T) {
	r := web.NewRouter()
	r.GET("/public", web.ServiceKey("the-key"), func(c *gin.Context) {
		c.String(http.StatusOK, web.ClientIP(c.Request.Context()).String())
	})
	res := send(r, http.MethodGet, "/public", "", "X-Service-Key", "the-key", "X-Visitor-IP", "::ffff:203.0.113.9")
	require.Equal(t, http.StatusOK, res.Code)
	require.Equal(t, "203.0.113.9", res.Body.String(), "the visitor's address, not the site server's")

	problem(t, send(r, http.MethodGet, "/public", ""), http.StatusUnauthorized, "unauthenticated")
	problem(t, send(r, http.MethodGet, "/public", "", "X-Service-Key", "wrong"), http.StatusUnauthorized, "unauthenticated")

	open := web.NewRouter()
	open.GET("/public", web.ServiceKey(""), func(c *gin.Context) { c.Status(http.StatusOK) })
	problem(t, send(open, http.MethodGet, "/public", "", "X-Service-Key", ""), http.StatusUnauthorized, "unauthenticated")
}

func TestSignedInWithoutSessionsRefusesEveryone(t *testing.T) {
	r := web.NewRouter()
	r.GET("/admin", web.SignedIn(nil), func(c *gin.Context) { c.Status(http.StatusOK) })
	problem(t, send(r, http.MethodGet, "/admin", "", "Authorization", "Bearer x"), http.StatusUnauthorized, "unauthenticated")
}

func TestLimit(t *testing.T) {
	rule := web.Rule{Name: "test-" + t.Name(), Limit: 2, Window: time.Minute, By: web.ByServiceKey}
	counter := ratelimit.New(redistest.Client(t), "test-"+time.Now().Format("150405.000000")+":", time.Now)
	r := web.NewRouter()
	r.GET("/limited", web.NewLimiter(counter).Limit(rule), func(c *gin.Context) { c.Status(http.StatusOK) })

	for range 2 {
		require.Equal(t, http.StatusOK, send(r, http.MethodGet, "/limited", "", "X-Service-Key", "k").Code)
	}
	res := send(r, http.MethodGet, "/limited", "", "X-Service-Key", "k")
	problem(t, res, http.StatusTooManyRequests, "rate_limited")
	require.NotEmpty(t, res.Header().Get("Retry-After"))
}

func TestLimitWithRedisDown(t *testing.T) {
	down := web.NewLimiter(nil)
	r := web.NewRouter()
	r.GET("/open", down.Limit(web.Rule{Name: "open", Limit: 1, Window: time.Minute, By: web.ByServiceKey}),
		func(c *gin.Context) { c.Status(http.StatusOK) })
	r.GET("/closed", down.Limit(web.Rule{Name: "closed", Limit: 1, Window: time.Minute, By: web.ByServiceKey, FailClosed: true}),
		func(c *gin.Context) { c.Status(http.StatusOK) })

	require.Equal(t, http.StatusOK, send(r, http.MethodGet, "/open", "").Code, "losing Redis never stops bookings")
	problem(t, send(r, http.MethodGet, "/closed", ""), http.StatusServiceUnavailable, "unavailable")
}
