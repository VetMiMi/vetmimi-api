package video_test

import (
	"log/slog"
	"maps"
	"net/http"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/VetMiMi/vetmimi-api/internal/pgtest"
	"github.com/VetMiMi/vetmimi-api/internal/video"
	"github.com/VetMiMi/vetmimi-api/internal/web"
	"github.com/VetMiMi/vetmimi-api/internal/webtest"
)

const siteOrigin = "https://vetmimi.example"

// videoAPI serves the video routes with the clock at now.
func videoAPI(t *testing.T, now time.Time) *webtest.API {
	clock := func() time.Time { return now }
	return webtest.New(t, func(r web.Routes) {
		pool := pgtest.Pool(t)
		h := &video.Handler{Pool: pool, Hub: video.NewHub(pool, slog.Default(), clock), Issuer: issuer,
			SiteURL: siteOrigin, Now: clock}
		h.Routes(r)
	})
}

func keys(v any) []string {
	return slices.Sorted(maps.Keys(v.(map[string]any)))
}

func TestHTTP_PublicSession(t *testing.T) {
	appt, token := withRoom(t)
	a := videoAPI(t, appt.StartsAt)

	got := webtest.JSON(t, http.StatusOK, a.Public(http.MethodGet, "/public/sessions/"+token, ""))
	require.Equal(t, []string{"closesAt", "endsAt", "locale", "opensAt", "service", "startsAt", "state", "timezone"},
		keys(got), "no visitor details")
	require.Equal(t, "ready", got["state"])
	require.Equal(t, map[string]any{"slug": "individual-art-therapy", "name": "တစ်ဦးချင်း အနုပညာကုထုံး"}, got["service"])

	ticket := webtest.JSON(t, http.StatusCreated, a.Public(http.MethodPost, "/public/sessions/"+token+"/ticket", ""))
	require.Equal(t, []string{"iceServers", "role", "roomId", "ticket", "ticketExpiresAt", "websocketUrl"}, keys(ticket))
	require.Equal(t, "client", ticket["role"])
	require.Equal(t, []any{map[string]any{"urls": []any{"stun:stun.l.google.com:19302"}}}, ticket["iceServers"])
	require.NotContains(t, a.Logs.String(), token)
	require.NotContains(t, a.Logs.String(), ticket["ticket"])

	p := webtest.Refused(t, http.StatusBadRequest, "invalid_request",
		a.Public(http.MethodGet, "/public/sessions/"+strings.Repeat("Z", 42), ""))
	require.Equal(t, "token", p["errors"].([]any)[0].(map[string]any)["field"])
	webtest.Refused(t, http.StatusNotFound, "not_found",
		a.Public(http.MethodPost, "/public/sessions/"+strings.Repeat("Z", 43)+"/ticket", ""))
}
