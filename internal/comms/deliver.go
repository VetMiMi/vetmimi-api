package comms

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"

	"github.com/VetMiMi/vetmimi-api/internal/db"
	"github.com/VetMiMi/vetmimi-api/internal/settings"
	"github.com/VetMiMi/vetmimi-api/internal/tokens"
	"github.com/VetMiMi/vetmimi-api/internal/video"
)

// attempt is what one delivery did to its row; an empty status means it did nothing.
type attempt struct {
	row    db.Communication
	status Status
	code   string // the skip reason or the error code
}

const maxAttempts = 5

const (
	SkipNotConfirmed   = "not_confirmed"
	SkipAlreadyStarted = "already_started"
	SkipSuperseded     = "superseded"
)

// deliver sends a due, queued row once and records the outcome on it.
func (t *Tasks) deliver(ctx context.Context, id pgtype.UUID) (attempt, error) {
	tx, err := t.Pool.Begin(ctx)
	if err != nil {
		return attempt{}, err
	}
	defer tx.Rollback(ctx)
	q := db.New(tx)

	// The row stays locked until commit, so a second worker skips it.
	row, err := q.LockCommunication(ctx, id)
	if errors.Is(err, pgx.ErrNoRows) {
		return attempt{}, nil
	}
	if err != nil {
		return attempt{}, err
	}
	now := t.Now()
	// A reminder moved later still has a task at its old time; the newer task sends it.
	if Status(row.Status) != StatusQueued || row.ScheduledFor.After(now) {
		return attempt{}, nil
	}
	current, err := settings.Load(ctx, q)
	if err != nil {
		return attempt{}, err
	}
	result, err := t.sendOrSkip(ctx, q, row, current, now)
	if err != nil {
		return attempt{}, err
	}
	err = recordAttempt(ctx, q, result)
	if err != nil {
		return attempt{}, err
	}

	err = tx.Commit(ctx)
	if err != nil {
		return attempt{}, err
	}
	return result, nil
}

// sendOrSkip sends the row, unless it is a reminder that must not go out.
func (t *Tasks) sendOrSkip(ctx context.Context, q *db.Queries, row db.Communication, current settings.Settings,
	now time.Time) (attempt, error) {
	skip, err := skipReason(ctx, q, row, current, now)
	if err != nil {
		return attempt{}, err
	}
	if skip != "" {
		result := attempt{
			row:    row,
			status: StatusCancelled,
			code:   skip,
		}
		return result, nil
	}

	email, err := t.compose(ctx, q, row, current)
	if err != nil {
		return attempt{}, err
	}
	providerID, sendErr := t.send(ctx, row, email, now)
	row.Attempts++
	if sendErr == nil {
		row.SentAt = sql.NullTime{Time: now, Valid: true}
		row.ProviderMessageID = pgtype.Text{String: providerID, Valid: true}
		return attempt{row: row, status: StatusSent}, nil
	}
	result := attempt{
		row:    row,
		status: StatusFailed,
		code:   errorCode(sendErr),
	}
	if row.Attempts < maxAttempts {
		result.status = StatusQueued
	}
	return result, nil
}

// recordAttempt writes the attempt to its locked, queued row, so setStatus cannot refuse it.
func recordAttempt(ctx context.Context, q *db.Queries, result attempt) error {
	if result.status == StatusQueued {
		return keepQueued(ctx, q, result.row, result.code)
	}
	_, err := setStatus(ctx, q, result.row, result.status, result.code)
	return err
}

// skipReason returns why row must not be sent, or "". Only reminders are skipped.
func skipReason(ctx context.Context, q *db.Queries, row db.Communication, current settings.Settings,
	now time.Time) (string, error) {
	if Kind(row.Kind) != Reminder {
		return "", nil
	}
	appointment, err := q.GetAppointmentForMessage(ctx, row.AppointmentID)
	if err != nil {
		return "", err
	}
	skip := reminderSkip(appointment, current.ReminderHours, row.ScheduledFor, now)
	// An admin's resend goes out at once, not at the reminder offset.
	if skip == SkipSuperseded && row.ResendOf.Valid {
		return "", nil
	}
	return skip, nil
}

// reminderSkip returns "" when the reminder may be sent.
func reminderSkip(appointment db.GetAppointmentForMessageRow, reminderHours int, scheduledFor, now time.Time) string {
	switch {
	case appointment.Status != "confirmed":
		return SkipNotConfirmed
	case !appointment.StartsAt.After(now):
		return SkipAlreadyStarted
	case !appointment.StartsAt.Add(-time.Duration(reminderHours) * time.Hour).Equal(scheduledFor):
		return SkipSuperseded
	}
	return ""
}

func (t *Tasks) send(ctx context.Context, row db.Communication, email Email, now time.Time) (string, error) {
	switch {
	case t.SMTP != nil:
		return t.SMTP.send(ctx, t.From, row, email, now)
	case t.Resend != nil:
		return t.sendResend(ctx, row, email)
	default:
		t.Log.InfoContext(ctx, "would send", "kind", row.Kind, "communication_id", row.ID.String())
		return "dev", nil
	}
}

func (t *Tasks) compose(ctx context.Context, q *db.Queries, row db.Communication, current settings.Settings) (Email, error) {
	var data RenderData
	var err error
	if row.ContactEnquiryID.Valid {
		data, err = t.enquiryData(ctx, q, row.ContactEnquiryID)
	} else {
		data, err = t.appointmentData(ctx, q, row, current)
	}
	if err != nil {
		return Email{}, err
	}
	email, err := Render(Kind(row.Kind), row.Locale, data)
	if err != nil {
		return Email{}, err
	}
	email.ReplyTo = current.ContactEmail
	return email, nil
}

func (t *Tasks) appointmentData(ctx context.Context, q *db.Queries, row db.Communication,
	current settings.Settings) (RenderData, error) {
	appointment, err := q.GetAppointmentForMessage(ctx, row.AppointmentID)
	if err != nil {
		return RenderData{}, err
	}
	kind := Kind(row.Kind)
	var previous time.Time
	if kind == Rescheduled {
		previousRange, err := q.PreviousRangeOf(ctx, row.AppointmentID)
		if err != nil && !errors.Is(err, pgx.ErrNoRows) {
			return RenderData{}, err
		}
		previous = previousRange.Lower.Time
	}
	data, err := DataFor(appointment, current, t.SiteURL, row.Locale, previous)
	if err != nil {
		return RenderData{}, err
	}

	if kind.Audience() == Practitioner {
		data.ClientMessage = row.Message.String
	} else {
		data.MessageToVisitor = row.Message.String
		token := tokens.Management(t.SigningSecret, appointment.ManagementTokenSeed)
		data.ManageURL = sitePath(t.SiteURL, row.Locale, "/manage/"+token)
		data.JoinURL, err = t.joinURL(ctx, q, appointment.ID, row.Locale)
		if err != nil {
			return RenderData{}, err
		}
	}
	if kind == PractitionerRescheduleRequested {
		data.PreferredTimes, err = preferredTimes(ctx, q, appointment, row.Locale)
		if err != nil {
			return RenderData{}, err
		}
	}
	return data, nil
}

func (t *Tasks) joinURL(ctx context.Context, q *db.Queries, appointmentID pgtype.UUID, locale string) (string, error) {
	room, ok, err := video.RoomOf(ctx, q, appointmentID)
	if err != nil {
		return "", err
	}
	if !ok {
		return "", nil
	}
	token := tokens.Join(t.SigningSecret, room.JoinTokenSeed)
	return sitePath(t.SiteURL, locale, "/session/"+token), nil
}

func preferredTimes(ctx context.Context, q *db.Queries, appointment db.GetAppointmentForMessageRow,
	locale string) ([]string, error) {
	raw, err := q.LatestRescheduleRequest(ctx, appointment.ID)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	var detail struct {
		Preferred []time.Time `json:"preferred"`
	}
	err = json.Unmarshal(raw, &detail)
	if err != nil {
		return nil, err
	}
	location, err := time.LoadLocation(appointment.Timezone)
	if err != nil {
		return nil, err
	}
	times := make([]string, len(detail.Preferred))
	for i, preferred := range detail.Preferred {
		times[i] = FormatTime(preferred, location, locale)
	}
	return times, nil
}
