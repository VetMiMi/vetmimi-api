package httpapi

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/VetMiMi/vetmimi-api/internal/booking"
	"github.com/VetMiMi/vetmimi-api/internal/db"
	"github.com/VetMiMi/vetmimi-api/internal/platform"
	"github.com/VetMiMi/vetmimi-api/internal/platform/pgtest"
)

// sendPublic sends body as the site does, with the service key and a
// visitor address of its own.
func (a *authAPI) sendPublic(method, path, body string, headers ...string) *httptest.ResponseRecorder {
	req := httptest.NewRequest(method, path, strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("X-Service-Key", testServiceKey)
	a.visitors++
	req.Header.Set("X-Visitor-IP", fmt.Sprintf("192.0.2.%d", a.visitors%250+1))
	for i := 0; i+1 < len(headers); i += 2 {
		req.Header.Set(headers[i], headers[i+1])
	}
	res := httptest.NewRecorder()
	a.handler.ServeHTTP(res, req)
	return res
}

// managedLink books the seeded therapy service at startsAt and returns its
// management token, derived as the worker derives it for the email.
func managedLink(t *testing.T, startsAt time.Time, status booking.Status) string {
	t.Helper()
	svc, err := db.New(pgtest.Pool(t)).GetServiceBySlug(context.Background(), "individual-art-therapy")
	require.NoError(t, err)
	appt := bookOnce(t, svc.ID.String(), startsAt, status)
	t.Cleanup(func() {
		_, err := pgtest.Pool(t).Exec(context.Background(), "DELETE FROM appointments WHERE id = $1", appt.ID)
		require.NoError(t, err)
	})
	return platform.NewManagementToken([]byte("test signing secret, 32 bytes ok"), appt.ManagementTokenSeed)
}

func TestManageLink(t *testing.T) {
	a := newAuthAPI(t)
	token := managedLink(t, time.Date(2027, 2, 16, 23, 0, 0, 0, time.UTC), booking.Confirmed)

	got := decoded(t, http.StatusOK, a.public("/public/manage/"+token))
	require.Equal(t, []string{"canCancel", "canRequestReschedule", "cancellationNoticeHours", "durationMinutes",
		"endsAt", "format", "lateIfCancelledNow", "reference", "rescheduleRequested", "service", "startsAt",
		"status", "timezone"}, keys(got), "no visitor details or notes")
	require.Equal(t, "confirmed", got["status"])
	require.Equal(t, true, got["canCancel"])
	require.Equal(t, false, got["lateIfCancelledNow"])

	requested := decoded(t, http.StatusAccepted, a.sendPublic(http.MethodPost, "/public/manage/"+token+"/reschedule-request",
		`{"preferredTimes": ["2027-02-17T23:00:00Z"], "message": "Mornings please"}`))
	require.Equal(t, true, requested["rescheduleRequested"])
	require.Equal(t, "confirmed", requested["status"], "the time stays booked")

	cancelled := decoded(t, http.StatusOK, a.sendPublic(http.MethodPost, "/public/manage/"+token+"/cancel",
		`{"message": "Sorry, something came up"}`))
	require.Equal(t, "cancelled_by_client", cancelled["status"])
	require.Equal(t, false, cancelled["canCancel"])
	refused(t, http.StatusUnprocessableEntity, "action_not_allowed",
		a.sendPublic(http.MethodPost, "/public/manage/"+token+"/cancel", ""))

	refused(t, http.StatusNotFound, "not_found", a.public("/public/manage/"+strings.Repeat("Z", 43)))
	refused(t, http.StatusBadRequest, "invalid_request", a.public("/public/manage/"+strings.Repeat("Z", 42)))
	requireUnauthenticated(t, a.send(http.MethodGet, "/public/manage/"+token, "not-a-service-key"))

	for _, private := range []string{token, "Mornings please", "something came up", "visitor@example.com"} {
		require.NotContains(t, a.logs.String(), private)
	}
}
