package booking

import (
	"context"
	"net/url"
	"strings"
	"time"

	"github.com/jackc/pgx/v5/pgtype"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/VetMiMi/vetmimi-api/internal/apperr"
	"github.com/VetMiMi/vetmimi-api/internal/db"
	"github.com/VetMiMi/vetmimi-api/internal/settings"
)

// manualLink is the meeting_link_mode in which Daw Mi pastes a link per appointment.
const manualLink = "manual_link"

var errMeetingLinkNotAllowed = apperr.New(apperr.ActionNotAllowed,
	"A meeting link can be set only for a future online appointment while meeting links are manual.")

// SetMeetingLink sets or replaces the link without emailing anyone; emails read the link when they are sent.
func SetMeetingLink(ctx context.Context, pool *pgxpool.Pool, change Change, link string, now time.Time) error {
	link, err := checkMeetingLink(link)
	if err != nil {
		return err
	}

	tx, err := pool.Begin(ctx)
	if err != nil {
		return err
	}
	defer tx.Rollback(ctx)
	q := db.New(tx)

	err = q.LockSchedule(ctx)
	if err != nil {
		return err
	}
	appointment, err := lockAppointment(ctx, q, change.ID, change.Version)
	if err != nil {
		return err
	}
	current, err := settings.Load(ctx, q)
	if err != nil {
		return err
	}
	_, err = setMeetingLink(ctx, q, appointment, link, current.MeetingLinkMode, change.Actor, now)
	if err != nil {
		return err
	}

	return tx.Commit(ctx)
}

// canSetMeetingLink is shared by the writes and the allowed actions, so the two always agree.
func canSetMeetingLink(mode string, status Status, format string, startsAt, now time.Time) bool {
	active := status == Pending || status == Confirmed
	return mode == manualLink && format == "online" && active && startsAt.After(now)
}

func setMeetingLink(ctx context.Context, q db.Querier, appointment db.Appointment, link, mode string,
	actor pgtype.UUID, now time.Time) (db.Appointment, error) {
	if !canSetMeetingLink(mode, Status(appointment.Status), appointment.Format, appointment.StartsAt, now) {
		return db.Appointment{}, errMeetingLinkNotAllowed
	}
	params := db.SetAppointmentMeetingLinkParams{
		ID:          appointment.ID,
		MeetingLink: link,
		Now:         now,
	}
	updated, err := q.SetAppointmentMeetingLink(ctx, params)
	if err != nil {
		return db.Appointment{}, err
	}
	event := Event{
		AppointmentID: appointment.ID,
		Kind:          "meeting_link_set",
		Actor:         "admin",
		ActorUserID:   actor,
	}
	err = AppendEvent(ctx, q, event)
	if err != nil {
		return db.Appointment{}, err
	}
	return updated, nil
}

func checkMeetingLink(link string) (string, error) {
	link = strings.TrimSpace(link)
	address, err := url.Parse(link)
	if err != nil || !strings.HasPrefix(link, "https://") || address.Host == "" {
		return "", apperr.Invalid("The meeting link is not valid.",
			apperr.FieldError{Field: "/meetingLink", Message: "must be a link that starts with https://"})
	}
	return link, nil
}
