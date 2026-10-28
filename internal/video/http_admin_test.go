package video_test

import (
	"net/http"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/VetMiMi/vetmimi-api/internal/webtest"
)

func TestHTTP_StartAndEndSession(t *testing.T) {
	appt, _ := withRoom(t)
	a := videoAPI(t, appt.StartsAt)
	admin := webtest.SignIn(t, "booking_admin")
	path := "/admin/appointments/" + appt.ID.String() + "/video-session"

	mine := webtest.JSON(t, http.StatusCreated, a.Admin(http.MethodPost, path, admin, ""))
	require.Equal(t, "practitioner", mine["role"])

	ended := webtest.JSON(t, http.StatusOK, a.Admin(http.MethodPost, path+"/end", admin, ""))
	require.Equal(t, []string{"closesAt", "endedAt", "id", "opensAt", "state"}, keys(ended), "startedAt only once both joined")
	require.Equal(t, "ended", ended["state"])
	require.Equal(t, mine["roomId"], ended["id"])

	webtest.Refused(t, http.StatusForbidden, "forbidden",
		a.Admin(http.MethodPost, path, webtest.SignIn(t, "content_editor"), ""))
	p := webtest.Refused(t, http.StatusBadRequest, "invalid_request",
		a.Admin(http.MethodPost, "/admin/appointments/not-a-uuid/video-session", admin, ""))
	require.Equal(t, "appointmentId", p["errors"].([]any)[0].(map[string]any)["field"])
}
