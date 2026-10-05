package httpapi

import (
	"net/http"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/VetMiMi/vetmimi-api/internal/auth"
)

// The per-email sign-in limits through the whole router. Every attempt here
// hashes a password, one at a time.

// Nine failures leave the email one short of a lockout: the right password
// still signs in.
func TestNineFailuresThenTheRightPasswordSignsIn(t *testing.T) {
	a := newAuthAPI(t)
	ad := a.newAdmin(t)
	for i := range 9 {
		res := a.signIn(t, ad.email, testPassword+"!", a.code(t, ad))
		require.Equal(t, http.StatusUnauthorized, res.Code, "failure %d: %s", i+1, res.Body.String())
	}
	signedIn(t, a.signIn(t, ad.email, testPassword, a.code(t, ad)))
}

// Ten failures lock an email whether or not it has an account, and the
// answer to the next attempt, even with the right password, gives nothing
// away: the same bytes, the same Retry-After, and a log line naming the email
// by a hash prefix only.
func TestLockedEmailAnswersTheSameWithOrWithoutAnAccount(t *testing.T) {
	a := newAuthAPI(t)
	ad := a.newAdmin(t)
	unknown := uniqueEmail(t)
	code := a.code(t, ad)

	var bodies []string
	var waits []int
	for _, email := range []string{ad.email, unknown} {
		for i := range 10 {
			res := a.signIn(t, email, testPassword+"!", code)
			require.Equal(t, http.StatusUnauthorized, res.Code, "failure %d: %s", i+1, res.Body.String())
		}
		res := a.signIn(t, email, testPassword, code)
		require.Equal(t, http.StatusTooManyRequests, res.Code, res.Body.String())
		require.Equal(t, "rate_limited", problemFrom(t, res)["code"])
		wait, err := strconv.Atoi(res.Header().Get("Retry-After"))
		require.NoError(t, err)
		bodies = append(bodies, res.Body.String())
		waits = append(waits, wait)
	}
	require.Equal(t, bodies[0], bodies[1])
	require.InDelta(t, waits[0], waits[1], 1)

	logs := a.logs.String()
	var locked []any
	for _, line := range logLines(t, logs) {
		if line["msg"] == "sign_in_locked" {
			locked = append(locked, line["email_hash"])
		}
	}
	require.Equal(t, []any{auth.EmailHashPrefix(ad.email), auth.EmailHashPrefix(unknown)}, locked)
	for _, secret := range []string{
		ad.email, strings.Split(ad.email, "@")[0], unknown, strings.Split(unknown, "@")[0], testPassword, code,
	} {
		require.NotContains(t, logs, secret)
	}

	a.clock.at = a.clock.at.Add(time.Hour)
	signedIn(t, a.signIn(t, ad.email, testPassword, a.code(t, ad)))
}
