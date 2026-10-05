package httpapi

import (
	"bytes"
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"strconv"
	"testing"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/stretchr/testify/require"
)

// readBody reads the whole body and answers its length, or the error the
// strict server would answer for it.
func readBody(w http.ResponseWriter, r *http.Request) {
	b, err := io.ReadAll(r.Body)
	if err != nil {
		requestError(w, r, err)
		return
	}
	_, _ = io.WriteString(w, strconv.Itoa(len(b)))
}

func post(t *testing.T, mount func(chi.Router), path string, size int) *httptest.ResponseRecorder {
	t.Helper()
	res, _ := through(t, mount, httptest.NewRequest(http.MethodPost, path, bytes.NewReader(make([]byte, size))))
	return res
}

func TestBodyAtTheCapPasses(t *testing.T) {
	res := post(t, func(r chi.Router) { r.Post("/save", readBody) }, "/save", 64<<10)
	require.Equal(t, http.StatusOK, res.Code)
	require.Equal(t, strconv.Itoa(64<<10), res.Body.String())
}

func TestBodyOverTheCapIs413(t *testing.T) {
	res := post(t, func(r chi.Router) { r.Post("/save", readBody) }, "/save", 64<<10+1)
	require.Equal(t, http.StatusRequestEntityTooLarge, res.Code)
	require.Equal(t, "payload_too_large", problemFrom(t, res)["code"])
}

func TestWithBodyCapReplacesTheGroupDefault(t *testing.T) {
	content := func(r chi.Router) { r.With(WithBodyCap(512<<10)).Post("/content", readBody) }

	res := post(t, content, "/content", 512<<10)
	require.Equal(t, http.StatusOK, res.Code)
	require.Equal(t, strconv.Itoa(512<<10), res.Body.String())

	res = post(t, content, "/content", 512<<10+1)
	require.Equal(t, http.StatusRequestEntityTooLarge, res.Code)
}

// deadline answers how long the request context had left when it arrived.
func deadline(w http.ResponseWriter, r *http.Request) {
	d, ok := r.Context().Deadline()
	if !ok {
		_, _ = io.WriteString(w, "none")
		return
	}
	_, _ = io.WriteString(w, time.Until(d).String())
}

func remaining(t *testing.T, mount func(chi.Router), path string) time.Duration {
	t.Helper()
	res, _ := through(t, mount, httptest.NewRequest(http.MethodGet, path, nil))
	d, err := time.ParseDuration(res.Body.String())
	require.NoError(t, err, res.Body.String())
	return d
}

func TestRequestsHaveTheDefaultDeadline(t *testing.T) {
	d := remaining(t, func(r chi.Router) { r.Get("/q", deadline) }, "/q")
	require.InDelta(t, 30*time.Second, d, float64(time.Second))
}

func TestWithTimeoutReplacesTheGroupDefault(t *testing.T) {
	d := remaining(t, func(r chi.Router) { r.With(WithTimeout(2*time.Minute)).Get("/upload", deadline) }, "/upload")
	require.InDelta(t, 2*time.Minute, d, float64(time.Second))
}

func TestDeadlineInAHandlerIs503(t *testing.T) {
	slow := func(r chi.Router) {
		r.With(WithTimeout(50*time.Millisecond)).Get("/slow", func(_ http.ResponseWriter, r *http.Request) {
			<-r.Context().Done()
		})
	}
	res, logs := through(t, slow, httptest.NewRequest(http.MethodGet, "/slow", nil))
	require.Equal(t, http.StatusServiceUnavailable, res.Code)
	require.Equal(t, "unavailable", problemFrom(t, res)["code"])
	requireSecurityHeaders(t, res.Header())
	require.Equal(t, float64(http.StatusServiceUnavailable), logs[0]["status"])
}

func TestAnswerBeforeTheDeadlineIsKept(t *testing.T) {
	late := func(r chi.Router) {
		r.With(WithTimeout(time.Millisecond)).Get("/late", func(w http.ResponseWriter, r *http.Request) {
			_, _ = io.WriteString(w, "done")
			<-r.Context().Done()
		})
	}
	res, _ := through(t, late, httptest.NewRequest(http.MethodGet, "/late", nil))
	require.Equal(t, http.StatusOK, res.Code)
	require.Equal(t, "done", res.Body.String())
}

func TestOnlyTheInnermostDeadlineCounts(t *testing.T) {
	// The handler outlives the outer deadline but not its own, and answers
	// nothing, which is an empty 200.
	h := WithTimeout(time.Millisecond)(WithTimeout(time.Minute)(http.HandlerFunc(
		func(http.ResponseWriter, *http.Request) { time.Sleep(20 * time.Millisecond) })))
	res := httptest.NewRecorder()
	h.ServeHTTP(res, httptest.NewRequest(http.MethodGet, "/", nil))
	require.Equal(t, http.StatusOK, res.Code)
	require.Empty(t, res.Body.String())
}

func TestTimeoutEndsWithTheRequest(t *testing.T) {
	gone := func(r chi.Router) {
		r.With(WithTimeout(time.Hour)).Get("/wait", func(w http.ResponseWriter, r *http.Request) {
			select {
			case <-r.Context().Done():
				_, _ = io.WriteString(w, r.Context().Err().Error())
			case <-time.After(time.Second):
				_, _ = io.WriteString(w, "still running")
			}
		})
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel() // the client has gone
	req := httptest.NewRequest(http.MethodGet, "/wait", nil).WithContext(ctx)
	res, _ := through(t, gone, req)
	require.Equal(t, context.Canceled.Error(), res.Body.String())
}
