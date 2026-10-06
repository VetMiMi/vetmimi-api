package booking

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"

	"github.com/VetMiMi/vetmimi-api/internal/db"
	"github.com/VetMiMi/vetmimi-api/internal/platform"
)

// Period is a half-open span of time, [Start, End), as every tstzrange in
// the schema is.
type Period struct {
	Start, End time.Time
}

func (p Period) tstzrange() pgtype.Range[pgtype.Timestamptz] {
	return pgtype.Range[pgtype.Timestamptz]{
		Lower:     pgtype.Timestamptz{Time: p.Start, Valid: true},
		Upper:     pgtype.Timestamptz{Time: p.End, Valid: true},
		LowerType: pgtype.Inclusive,
		UpperType: pgtype.Exclusive,
		Valid:     true,
	}
}

// PeriodOf reads a tstzrange column.
func PeriodOf(r pgtype.Range[pgtype.Timestamptz]) Period {
	return Period{Start: r.Lower.Time, End: r.Upper.Time}
}

// BusyRange is the time an appointment keeps the practitioner from others:
// the session plus the service's buffers. Only this function computes it, at
// write time, so changing a service's buffers leaves booked rows alone.
func BusyRange(startsAt time.Time, duration time.Duration, s db.Service) Period {
	return Period{
		Start: startsAt.Add(-time.Duration(s.BufferBeforeMinutes) * time.Minute),
		End:   startsAt.Add(duration + time.Duration(s.BufferAfterMinutes)*time.Minute),
	}
}

// NewAppointment is what a caller decides about a new appointment; the
// session end, busy range, reference and management token follow from it.
type NewAppointment struct {
	PractitionerID pgtype.UUID
	Service        db.Service
	StartsAt       time.Time
	Duration       time.Duration
	Status         Status
	Timezone       string
	Format         string
	Locale         string
	Source         string
	VisitorName    string
	VisitorEmail   string
	VisitorPhone   pgtype.Text
	VisitorNote    pgtype.Text
	PrivacyAckAt   sql.NullTime
	PolicyAckAt    sql.NullTime
	HoldExpiresAt  sql.NullTime
	CreatedBy      pgtype.UUID
}

// referenceAttempts bounds the retries on a reference collision, which at a
// few hundred appointments a year should never need a second.
const referenceAttempts = 5

// InsertAppointment inserts a, deriving its management token from a fresh
// seed under secret. A time that overlaps another pending or confirmed
// appointment is refused by appointments_no_overlap as slot_unavailable.
// The caller owns the transaction, the schedule lock and the created event.
func InsertAppointment(ctx context.Context, q db.Querier, secret []byte, a NewAppointment) (db.Appointment, error) {
	seed, err := NewSeed()
	if err != nil {
		return db.Appointment{}, err
	}
	p := db.InsertAppointmentParams{
		PractitionerID:      a.PractitionerID,
		ServiceID:           a.Service.ID,
		Status:              string(a.Status),
		StartsAt:            a.StartsAt,
		EndsAt:              a.StartsAt.Add(a.Duration),
		DurationMinutes:     int32(a.Duration / time.Minute),
		BusyRange:           BusyRange(a.StartsAt, a.Duration, a.Service).tstzrange(),
		Timezone:            a.Timezone,
		Format:              a.Format,
		Locale:              a.Locale,
		Source:              a.Source,
		VisitorName:         a.VisitorName,
		VisitorEmail:        a.VisitorEmail,
		VisitorPhone:        a.VisitorPhone,
		VisitorNote:         a.VisitorNote,
		PrivacyAckAt:        a.PrivacyAckAt,
		PolicyAckAt:         a.PolicyAckAt,
		HoldExpiresAt:       a.HoldExpiresAt,
		ManagementTokenSeed: seed,
		ManagementTokenHash: platform.HashToken(NewManagementToken(secret, seed)),
		CreatedBy:           a.CreatedBy,
	}
	for range referenceAttempts {
		if p.Reference, err = NewReference(); err != nil {
			return db.Appointment{}, err
		}
		row, err := q.InsertAppointment(ctx, p)
		if !errors.Is(err, pgx.ErrNoRows) {
			return row, refusal(err)
		}
	}
	return db.Appointment{}, fmt.Errorf("booking: no free reference after %d attempts", referenceAttempts)
}
