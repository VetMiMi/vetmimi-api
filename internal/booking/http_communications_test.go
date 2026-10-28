package booking_test

import (
	"context"
	"net/http"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/VetMiMi/vetmimi-api/internal/booking"
	"github.com/VetMiMi/vetmimi-api/internal/pgtest"
	"github.com/VetMiMi/vetmimi-api/internal/webtest"
)

func TestHTTP_Communications(t *testing.T) {
	appt, _ := booked(t, booking.Confirmed)
	a := bookingAPI(t)
	token := webtest.SignIn(t, "booking_admin")
	path := "/admin/appointments/" + appt.ID.String()

	manual := webtest.JSON(t, http.StatusCreated, a.Admin(http.MethodPost, path+"/mark-communicated", token,
		`{"kind": "booking_confirmed", "note": "Phoned her"}`))
	require.Equal(t, "manual", manual["channel"])
	require.Equal(t, "Phoned her", manual["note"])
	require.Equal(t, appt.ID.String(), manual["appointmentId"])
	p := webtest.Refused(t, http.StatusBadRequest, "invalid_request",
		a.Admin(http.MethodPost, path+"/mark-communicated", token, `{"kind": "booking_confirmed", "note": ""}`))
	require.Equal(t, []any{map[string]any{"field": "/note", "message": "is required"}}, p["errors"])

	// The reminder booked() queued has not been sent, so there is nothing to resend.
	var reminder string
	require.NoError(t, pgtest.Pool(t).QueryRow(context.Background(),
		"SELECT id::text FROM communications WHERE appointment_id = $1 AND kind = 'reminder'", appt.ID).Scan(&reminder))
	webtest.Refused(t, http.StatusUnprocessableEntity, "action_not_allowed",
		a.Admin(http.MethodPost, path+"/communications/"+reminder+"/resend", token, ""))
}
