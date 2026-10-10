package comms

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"expvar"
	"fmt"
	"log/slog"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/resend/resend-go/v2"

	"github.com/VetMiMi/vetmimi-api/internal/db"
	"github.com/VetMiMi/vetmimi-api/internal/platform"
	"github.com/VetMiMi/vetmimi-api/internal/platform/clock"
	"github.com/VetMiMi/vetmimi-api/internal/platform/settings"
	"github.com/VetMiMi/vetmimi-api/internal/video"
)

const maxAttempts = 5

const (
	SkipNotConfirmed   = "not_confirmed"
	SkipAlreadyStarted = "already_started"
	SkipSuperseded     = "superseded"
)

var emailsFailed = expvar.NewInt("emails_failed")

type Tasks struct {
	Pool          *pgxpool.Pool
	Queue         *platform.Queue
	Resend        *resend.Client // nil in development: sends are only logged
	From          string
	SiteURL       string
	SigningSecret []byte
	Log           *slog.Logger
	Now           clock.Now
}

func (t *Tasks) Register(w *platform.Worker) {
	w.Handle(TaskDeliver, t.Deliver)
	w.Handle(TaskSweep, t.Sweep)
	w.Handle(TaskRescheduleReminders, t.RescheduleReminders)
	w.Every("@every 5m", TaskSweep)
}

// Deliver returns an error only when asynq should retry.
func (t *Tasks) Deliver(ctx context.Context, payload []byte) error {
	id, err := parseDeliverPayload(payload)
	if err != nil {
		return err
	}

	tx, err := t.Pool.Begin(ctx)
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback(ctx) }()
	q := db.New(tx)

	// The row stays locked until commit, so a second worker skips it.
	row, err := q.LockCommunication(ctx, id)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil
	}
	if err != nil {
		return err
	}
	now := t.Now()
	// A reminder moved later still has a task at its old time; the newer task sends it.
	if Status(row.Status) != StatusQueued || row.ScheduledFor.After(now) {
		return nil
	}

	s, err := settings.Load(ctx, q)
	if err != nil {
		return err
	}
	skip, err := skipReason(ctx, q, row, s, now)
	if err != nil {
		return err
	}
	if skip != "" {
		return t.recordSkipped(ctx, tx, row, skip)
	}

	email, err := t.compose(ctx, q, row, s)
	if err != nil {
		return err
	}
	return t.sendAndRecord(ctx, tx, row, email, now)
}

func parseDeliverPayload(payload []byte) (pgtype.UUID, error) {
	var p deliverPayload
	var id pgtype.UUID
	if err := json.Unmarshal(payload, &p); err != nil {
		return id, fmt.Errorf("comms: deliver payload: %w", err)
	}
	if err := id.Scan(p.CommunicationID); err != nil {
		return id, fmt.Errorf("comms: deliver payload: %w", err)
	}
	return id, nil
}

// skipReason returns why row must not be sent, or "". Only reminders are skipped.
func skipReason(ctx context.Context, q *db.Queries, row db.Communication, s settings.Settings,
	now time.Time) (string, error) {
	if Kind(row.Kind) != Reminder {
		return "", nil
	}
	appt, err := q.GetAppointmentForMessage(ctx, row.AppointmentID)
	if err != nil {
		return "", err
	}
	return reminderSkip(appt, s.ReminderHours, row.ScheduledFor, now), nil
}

func (t *Tasks) sendAndRecord(ctx context.Context, tx pgx.Tx, row db.Communication, email Email, now time.Time) error {
	providerID, err := t.send(ctx, row, email)
	row.Attempts++
	switch {
	case err == nil:
		return t.recordSent(ctx, tx, row, providerID, now)
	case row.Attempts < maxAttempts:
		return t.recordRetry(ctx, tx, row, errorCode(err))
	default:
		return t.recordFailed(ctx, tx, row, errorCode(err))
	}
}

func (t *Tasks) recordSkipped(ctx context.Context, tx pgx.Tx, row db.Communication, skip string) error {
	if err := finish(ctx, tx, row, StatusCancelled, skip); err != nil {
		return err
	}
	t.Log.InfoContext(ctx, "communication skipped", "kind", row.Kind,
		"communication_id", row.ID.String(), "skip", skip)
	return nil
}

func (t *Tasks) recordSent(ctx context.Context, tx pgx.Tx, row db.Communication, providerID string, now time.Time) error {
	row.SentAt = sql.NullTime{Time: now, Valid: true}
	row.ProviderMessageID = pgtype.Text{String: providerID, Valid: true}
	if err := finish(ctx, tx, row, StatusSent, ""); err != nil {
		return err
	}
	t.Log.InfoContext(ctx, "email sent", "kind", row.Kind, "communication_id", row.ID.String())
	return nil
}

// recordRetry returns an error so asynq runs the task again.
func (t *Tasks) recordRetry(ctx context.Context, tx pgx.Tx, row db.Communication, code string) error {
	if err := keepQueued(ctx, db.New(tx), row, code); err != nil {
		return err
	}
	if err := tx.Commit(ctx); err != nil {
		return err
	}
	return fmt.Errorf("comms: send failed: %s", code)
}

func (t *Tasks) recordFailed(ctx context.Context, tx pgx.Tx, row db.Communication, code string) error {
	if err := finish(ctx, tx, row, StatusFailed, code); err != nil {
		return err
	}
	emailsFailed.Add(1)
	t.Log.ErrorContext(ctx, "email failed", "kind", row.Kind, "communication_id", row.ID.String(),
		"attempts", row.Attempts, "error_code", code)
	return nil
}

// finish moves the locked, queued row to its final status and commits; the
// lock means setStatus cannot refuse it.
func finish(ctx context.Context, tx pgx.Tx, row db.Communication, to Status, code string) error {
	if _, err := setStatus(ctx, db.New(tx), row, to, code); err != nil {
		return err
	}
	return tx.Commit(ctx)
}

func (t *Tasks) compose(ctx context.Context, q *db.Queries, row db.Communication, s settings.Settings) (Email, error) {
	var data RenderData
	var err error
	if row.ContactEnquiryID.Valid {
		data, err = t.enquiryData(ctx, q, row.ContactEnquiryID)
	} else {
		data, err = t.appointmentData(ctx, q, row, s)
	}
	if err != nil {
		return Email{}, err
	}
	email, err := Render(Kind(row.Kind), row.Locale, data)
	if err != nil {
		return Email{}, err
	}
	email.ReplyTo = s.ContactEmail
	return email, nil
}

func (t *Tasks) appointmentData(ctx context.Context, q *db.Queries, row db.Communication, s settings.Settings) (RenderData, error) {
	appt, err := q.GetAppointmentForMessage(ctx, row.AppointmentID)
	if err != nil {
		return RenderData{}, err
	}
	kind := Kind(row.Kind)
	var previous time.Time
	if kind == Rescheduled {
		r, err := q.PreviousRangeOf(ctx, row.AppointmentID)
		if err != nil && !errors.Is(err, pgx.ErrNoRows) {
			return RenderData{}, err
		}
		previous = r.Lower.Time
	}
	data, err := DataFor(appt, s, t.SiteURL, row.Locale, previous)
	if err != nil {
		return RenderData{}, err
	}

	if kind.Audience() == Practitioner {
		data.ClientMessage = row.Message.String
	} else {
		data.MessageToVisitor = row.Message.String
		token := platform.NewManagementToken(t.SigningSecret, appt.ManagementTokenSeed)
		data.ManageURL = sitePath(t.SiteURL, row.Locale, "/manage/"+token)
		if data.JoinURL, err = t.joinURL(ctx, q, appt.ID, row.Locale); err != nil {
			return RenderData{}, err
		}
	}
	if kind == PractitionerRescheduleRequested {
		if data.PreferredTimes, err = preferredTimes(ctx, q, appt, row.Locale); err != nil {
			return RenderData{}, err
		}
	}
	return data, nil
}

// reminderSkip returns "" when the reminder may be sent.
func reminderSkip(appt db.GetAppointmentForMessageRow, reminderHours int, scheduledFor, now time.Time) string {
	switch {
	case appt.Status != "confirmed":
		return SkipNotConfirmed
	case !appt.StartsAt.After(now):
		return SkipAlreadyStarted
	case !appt.StartsAt.Add(-time.Duration(reminderHours) * time.Hour).Equal(scheduledFor):
		return SkipSuperseded
	}
	return ""
}

func (t *Tasks) joinURL(ctx context.Context, q *db.Queries, appointmentID pgtype.UUID, locale string) (string, error) {
	room, ok, err := video.RoomOf(ctx, q, appointmentID)
	if err != nil {
		return "", err
	}
	if !ok {
		return "", nil
	}
	token := platform.NewJoinToken(t.SigningSecret, room.JoinTokenSeed)
	return sitePath(t.SiteURL, locale, "/session/"+token), nil
}

func preferredTimes(ctx context.Context, q *db.Queries, appt db.GetAppointmentForMessageRow, locale string) ([]string, error) {
	raw, err := q.LatestRescheduleRequest(ctx, appt.ID)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	var detail struct {
		Preferred []time.Time `json:"preferred"`
	}
	if err := json.Unmarshal(raw, &detail); err != nil {
		return nil, err
	}
	loc, err := time.LoadLocation(appt.Timezone)
	if err != nil {
		return nil, err
	}
	out := make([]string, len(detail.Preferred))
	for i, p := range detail.Preferred {
		out[i] = FormatTime(p, loc, locale)
	}
	return out, nil
}
