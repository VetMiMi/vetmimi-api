package httpapi

import (
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestHealthz(t *testing.T) {
	srv := httptest.NewServer(Handler())
	defer srv.Close()

	res, err := http.Get(srv.URL + "/healthz")
	require.NoError(t, err)
	defer res.Body.Close()

	require.Equal(t, http.StatusOK, res.StatusCode)
	require.Equal(t, "application/json", res.Header.Get("Content-Type"))
}

func TestUnknownRouteIs404(t *testing.T) {
	srv := httptest.NewServer(Handler())
	defer srv.Close()

	res, err := http.Get(srv.URL + "/nope")
	require.NoError(t, err)
	defer res.Body.Close()

	require.Equal(t, http.StatusNotFound, res.StatusCode)
}
