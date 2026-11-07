package booking

import (
	"context"
	"errors"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/VetMiMi/vetmimi-api/internal/apperr"
	"github.com/VetMiMi/vetmimi-api/internal/comms"
	"github.com/VetMiMi/vetmimi-api/internal/db"
	"github.com/VetMiMi/vetmimi-api/internal/queue"
	"github.com/VetMiMi/vetmimi-api/internal/settings"
	"github.com/VetMiMi/vetmimi-api/internal/tokens"
	"github.com/VetMiMi/vetmimi-api/internal/video"
)

// Managed is what a management link shows: never the visitor's details or any note.
type Managed struct {
	Reference               string     `json:"reference"`
	Status                  Status     `json:"status"`
	Service                 ServiceRef `json:"service"`
	StartsAt                time.Time  `json:"startsAt"`
	EndsAt                  time.Time  `json:"endsAt"`
	DurationMinutes         int        `json:"durationMinutes"`
	Timezone                string     `json:"timezone"`
	Format                  string     `json:"format"`
	CanCancel               bool       `json:"canCancel"`
	CanRequestReschedule    bool       `json:"canRequestReschedule"`
	CancellationNoticeHours int        `json:"cancellationNoticeHours"`
	LateIfCancelledNow      bool       `json:"lateIfCancelledNow"`
	RescheduleRequested     bool       `json:"rescheduleRequested"`
}

var (
	// The same for an unknown and an expired link, so a link never reveals whether an appointment exists.
	errLinkNotFound   = apperr.New(apperr.NotFound, "This link is not valid or has expired.")
	errLinkNotAllowed = apperr.New(apperr.ActionNotAllowed, "This appointment can no longer be changed through its link.")
)

func GetManaged(ctx context.Context, q db.Querier, token string, now time.Time) (Managed, error) {
	row, err := q.GetManagedAppointment(ctx, tokens.Hash(token))
	if errors.Is(err, pgx.ErrNoRows) {
		return Managed{}, errLinkNotFound
	}
	if err != nil {
		return Managed{}, err
	}
	status := Status(row.Status)
	if linkExpired(status, row.EndsAt, now) {
		return Managed{}, errLinkNotFound
	}
	current, err := settings.Load(ctx, q)
	if err != nil {
		return Managed{}, err
	}
	requested, err := q.RescheduleRequestOpen(ctx, row.ID)
	if err != nil {
		return Managed{}, err
	}
	notice := time.Duration(current.CancellationNoticeHours) * time.Hour
	changeable := actionable(status, row.StartsAt, now)
	managed := Managed{
		Reference:               row.Reference,
		Status:                  status,
		Service:                 ServiceRef{Slug: row.ServiceSlug, Name: inLocale(row.ServiceName, row.Locale)},
		StartsAt:                row.StartsAt.UTC(),
		EndsAt:                  row.EndsAt.UTC(),
		DurationMinutes:         int(row.DurationMinutes),
		Timezone:                row.Timezone,
		Format:                  row.Format,
		CanCancel:               changeable,
		CanRequestReschedule:    changeable,
		CancellationNoticeHours: current.CancellationNoticeHours,
		LateIfCancelledNow:      changeable && late(row.StartsAt, now, notice),
		RescheduleRequested:     requested,
	}
	return managed, nil
}

// CancelByClient cancels through a management link, recording it as late inside the cancellation notice.
func CancelByClient(ctx context.Context, pool *pgxpool.Pool, token, message string, now time.Time) (Changed, error) {
	tx, err := pool.Begin(ctx)
	if err != nil {
		return Changed{}, err
	}
	defer tx.Rollback(ctx)
	q := db.New(tx)

	err = q.LockSchedule(ctx)
	if err != nil {
		return Changed{}, err
	}
	appointment, err := lockByToken(ctx, q, token, now)
	if err != nil {
		return Changed{}, err
	}
	from := Status(appointment.Status)
	if !CanTransition(from, CancelledByClient) {
		return Changed{}, errLinkNotAllowed
	}
	current, err := settings.Load(ctx, q)
	if err != nil {
		return Changed{}, err
	}
	isLate := late(appointment.StartsAt, now, time.Duration(current.CancellationNoticeHours)*time.Hour)
	params := db.CancelAppointmentByClientParams{
		ID:               appointment.ID,
		LateCancellation: isLate,
		Now:              now,
	}
	_, err = q.CancelAppointmentByClient(ctx, params)
	if err != nil {
		return Changed{}, err
	}
	event := Event{
		AppointmentID: appointment.ID,
		Kind:          "cancelled",
		From:          from,
		To:            CancelledByClient,
		Actor:         "visitor",
		Detail:        EventDetail{By: "client", LateCancellation: &isLate},
	}
	err = AppendEvent(ctx, q, event)
	if err != nil {
		return Changed{}, err
	}
	err = CancelReminders(ctx, q, appointment.ID, string(CancelledByClient))
	if err != nil {
		return Changed{}, err
	}
	room, err := video.EndRoom(ctx, q, appointment.ID, video.EndedByCancellation, now)
	if err != nil {
		return Changed{}, err
	}
	tasks, err := queueClientCancelled(ctx, q, appointment, current, message)
	if err != nil {
		return Changed{}, err
	}

	err = tx.Commit(ctx)
	if err != nil {
		return Changed{}, err
	}
	changed := Changed{
		Tasks:     tasks,
		EndedRoom: room,
	}
	if from == Pending {
		changed.Remove = []queue.Task{holdTask(appointment.ID, time.Time{})}
	}
	return changed, nil
}

// RequestReschedule only tells Daw Mi; the appointment keeps its time until she moves it.
func RequestReschedule(ctx context.Context, pool *pgxpool.Pool, token string, preferred []time.Time, message string,
	now time.Time) (Changed, error) {
	tx, err := pool.Begin(ctx)
	if err != nil {
		return Changed{}, err
	}
	defer tx.Rollback(ctx)
	q := db.New(tx)

	appointment, err := lockByToken(ctx, q, token, now)
	if err != nil {
		return Changed{}, err
	}
	current, err := settings.Load(ctx, q)
	if err != nil {
		return Changed{}, err
	}
	utc := make([]time.Time, len(preferred))
	for i, at := range preferred {
		utc[i] = at.UTC()
	}
	event := Event{
		AppointmentID: appointment.ID,
		Kind:          "reschedule_requested",
		Actor:         "visitor",
		Detail:        EventDetail{Preferred: utc},
	}
	err = AppendEvent(ctx, q, event)
	if err != nil {
		return Changed{}, err
	}
	practitioner := comms.Message{
		AppointmentID: appointment.ID,
		Kind:          comms.PractitionerRescheduleRequested,
		Recipient:     current.ContactEmail,
		Text:          strings.TrimSpace(message),
	}
	task, err := comms.Queue(ctx, q, practitioner)
	if err != nil {
		return Changed{}, err
	}

	err = tx.Commit(ctx)
	if err != nil {
		return Changed{}, err
	}
	return Changed{Tasks: []queue.Task{task}}, nil
}

func queueClientCancelled(ctx context.Context, q db.Querier, appointment db.Appointment, current settings.Settings,
	message string) ([]queue.Task, error) {
	toVisitor := comms.Message{
		AppointmentID: appointment.ID,
		Kind:          comms.Cancelled,
		Recipient:     appointment.VisitorEmail,
		Locale:        appointment.Locale,
	}
	visitor, err := comms.Queue(ctx, q, toVisitor)
	if err != nil {
		return nil, err
	}
	toPractitioner := comms.Message{
		AppointmentID: appointment.ID,
		Kind:          comms.PractitionerClientCancelled,
		Recipient:     current.ContactEmail,
		Text:          strings.TrimSpace(message),
	}
	practitioner, err := comms.Queue(ctx, q, toPractitioner)
	if err != nil {
		return nil, err
	}
	return []queue.Task{visitor, practitioner}, nil
}

func lockByToken(ctx context.Context, q db.Querier, token string, now time.Time) (db.Appointment, error) {
	appointment, err := q.LockAppointmentByTokenHash(ctx, tokens.Hash(token))
	if errors.Is(err, pgx.ErrNoRows) {
		return db.Appointment{}, errLinkNotFound
	}
	if err != nil {
		return db.Appointment{}, err
	}
	status := Status(appointment.Status)
	if linkExpired(status, appointment.EndsAt, now) {
		return db.Appointment{}, errLinkNotFound
	}
	if !actionable(status, appointment.StartsAt, now) {
		return db.Appointment{}, errLinkNotAllowed
	}
	return appointment, nil
}

func linkExpired(status Status, endsAt, now time.Time) bool {
	return Terminal(status) && endsAt.Before(now)
}

func actionable(status Status, startsAt, now time.Time) bool {
	return (status == Pending || status == Confirmed) && startsAt.After(now)
}

// late compares real hours, so across a daylight-saving change the wall-clock gap may differ.
func late(startsAt, now time.Time, notice time.Duration) bool {
	return startsAt.Sub(now) < notice
}
