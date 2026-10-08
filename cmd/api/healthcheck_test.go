package main

import (
	"net"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os/exec"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

func TestHealthcheckPassesWhenHealthy(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/healthz" {
			http.NotFound(w, r)
		}
	}))
	defer srv.Close()

	require.Equal(t, 0, healthcheck(portOf(t, srv.URL)))
}

func TestHealthcheckFailsWhenUnhealthy(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusServiceUnavailable)
	}))
	defer srv.Close()

	require.Equal(t, 1, healthcheck(portOf(t, srv.URL)))
}

func TestHealthcheckFailsWhenDown(t *testing.T) {
	l, err := net.Listen("tcp", "127.0.0.1:0")
	require.NoError(t, err)
	_, port, _ := net.SplitHostPort(l.Addr().String())
	require.NoError(t, l.Close())

	start := time.Now()
	require.Equal(t, 1, healthcheck(port))
	require.Less(t, time.Since(start), 3*time.Second)
}

// The live image has no zoneinfo, so Australia/Sydney loads only if the
// binary embeds it; the laptop and CI have zoneinfo and cannot show that.
// The import is checked on main itself, not left to a dependency.
func TestEmbedsTimeZoneData(t *testing.T) {
	out, err := exec.Command("go", "list", "-f", `{{join .Imports " "}}`, ".").Output()
	require.NoError(t, err)
	require.Contains(t, strings.Fields(string(out)), "time/tzdata")
}

func portOf(t *testing.T, raw string) string {
	t.Helper()
	u, err := url.Parse(raw)
	require.NoError(t, err)
	return u.Port()
}
