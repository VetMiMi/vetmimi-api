package httpapi

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/VetMiMi/vetmimi-api/internal/platform"
	"github.com/VetMiMi/vetmimi-api/internal/platform/pgtest"
)

// mountAPI runs the key check, the session check, the rate limit and
// validation, in that order.

// The key check runs before the limiter: a caller without the key is refused
// uncounted.
func TestKeylessSignInIsNotCounted(t *testing.T) {
	a := liveAPI(t)
	for range 10 {
		res := a.send(http.MethodGet, signInURL, "X-Visitor-IP", visitorIP)
		requireUnauthenticated(t, res)
	}
	require.Empty(t, a.keys(t))
	for range 5 {
		require.Equal(t, http.StatusOK, a.signIn(visitorIP).Code)
	}
	requireRateLimited(t, a.signIn(visitorIP))
}

// The session check runs before the limiter, so callers without a session
// are refused without being counted.
func TestTokenlessCallsAreRefusedUncounted(t *testing.T) {
	a := liveAPI(t)
	for range 10 {
		requireUnauthenticated(t, a.send(http.MethodGet, meURL))
	}
	require.Empty(t, a.keys(t))
}

// The whole order. Each step puts two of the checks against each other, and
// the earlier one answers.
func TestMiddlewaresRunKeySessionLimitValidation(t *testing.T) {
	a := liveAPI(t)
	token := a.newSession(t)
	malformed := func(headers ...string) *httptest.ResponseRecorder {
		return a.send(http.MethodGet, "/auth/me", headers...)
	}

	// Session before limit and validation: not counted, and 401 rather than 400.
	requireUnauthenticated(t, malformed())
	require.Empty(t, a.keys(t))

	// Limit before validation: a malformed call with a session is counted,
	// then refused as malformed, and over the limit it is 429.
	require.Equal(t, []map[string]any{fieldError("probe", "is required")}, invalid(t, malformed(bearer(token)...)))
	require.Len(t, a.keys(t), 1)
	for i := range 299 {
		require.Equal(t, http.StatusOK, a.send(http.MethodGet, meURL, bearer(token)...).Code, "request %d", i+2)
	}
	requireRateLimited(t, malformed(bearer(token)...))

	// Session before limit: the token is over its limit, but once its
	// session is gone it is refused as unauthenticated.
	_, err := pgtest.Pool(t).Exec(context.Background(), "DELETE FROM sessions WHERE token_hash = $1", platform.HashToken(token))
	require.NoError(t, err)
	requireUnauthenticated(t, a.send(http.MethodGet, meURL, bearer(token)...))

	// Key before limit: over the sign-in limit, a call without the key is
	// 401 and not counted.
	for range 5 {
		require.Equal(t, http.StatusOK, a.signIn(visitorIP).Code)
	}
	requireRateLimited(t, a.signIn(visitorIP))
	before := a.keys(t)
	requireUnauthenticated(t, a.send(http.MethodGet, signInURL, "X-Visitor-IP", visitorIP))
	require.ElementsMatch(t, before, a.keys(t))
}

func TestOverTheLimitIs429BeforeValidation(t *testing.T) {
	a := liveAPI(t)
	malformed := func() *httptest.ResponseRecorder {
		return a.send(http.MethodGet, "/healthz", "X-Service-Key", testServiceKey, "X-Visitor-IP", visitorIP)
	}
	for range 5 {
		require.Equal(t, []map[string]any{fieldError("probe", "is required")}, invalid(t, malformed()))
	}
	requireRateLimited(t, malformed())
}
