package auth

import (
	"context"
	"errors"
	"strings"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgtype"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/VetMiMi/vetmimi-api/internal/db"
)

// Roles a user may hold (ADR-002). The users_roles_check constraint lists the
// same three.
var Roles = []string{"content_editor", "booking_admin", "site_admin"}

// ErrPractitionerTaken means another user is already the practitioner;
// appointments are booked with exactly one person.
var ErrPractitionerTaken = errors.New("another user is already the practitioner; there can be only one")

// Account is everything create-user saves for one administrator.
type Account struct {
	Email        string
	DisplayName  string
	PasswordHash string
	Roles        []string
	Practitioner bool
	// SealedTOTPSecret is nil for a password-only account (issue #143).
	SealedTOTPSecret []byte
	// EnrolmentStep is the step of the code typed at enrolment. It is saved
	// as the last accepted step, so that code cannot be replayed to sign in.
	EnrolmentStep int64
}

// NormalizeEmail is the form emails are stored and looked up in.
func NormalizeEmail(email string) string {
	return strings.ToLower(strings.TrimSpace(email))
}

// SaveAccount creates the account, or, when its email already exists,
// replaces that user's password, TOTP secret (clearing it for a
// password-only account), display name and roles,
// replaces the last accepted TOTP step with the enrolment step, and deletes
// the user's sessions, all in one transaction: a new password signs the user
// out everywhere, and a failed save changes nothing. It reports whether it
// created the user.
func SaveAccount(ctx context.Context, pool *pgxpool.Pool, a Account) (id pgtype.UUID, created bool, err error) {
	email := NormalizeEmail(a.Email)
	step := pgtype.Int8{Int64: a.EnrolmentStep, Valid: a.SealedTOTPSecret != nil}
	err = pgx.BeginFunc(ctx, pool, func(tx pgx.Tx) error {
		q := db.New(tx)
		id, err = q.ReplaceUserCredentials(ctx, db.ReplaceUserCredentialsParams{
			Email:          email,
			DisplayName:    a.DisplayName,
			PasswordHash:   a.PasswordHash,
			Roles:          a.Roles,
			IsPractitioner: a.Practitioner,
			TotpSecretEnc:  a.SealedTOTPSecret,
			TotpLastStep:   step,
		})
		if err == nil {
			return q.DeleteUserSessions(ctx, id)
		}
		if !errors.Is(err, pgx.ErrNoRows) {
			return practitionerTaken(err)
		}
		id, err = q.CreateUser(ctx, db.CreateUserParams{
			Email:          email,
			DisplayName:    a.DisplayName,
			PasswordHash:   a.PasswordHash,
			Roles:          a.Roles,
			IsPractitioner: a.Practitioner,
			TotpSecretEnc:  a.SealedTOTPSecret,
			TotpLastStep:   step,
		})
		created = err == nil
		return practitionerTaken(err)
	})
	if err != nil {
		return pgtype.UUID{}, false, err
	}
	return id, created, nil
}

// practitionerTaken turns a violation of the one-practitioner index into
// ErrPractitionerTaken. The index, not a lookup first, decides, so two
// concurrent runs cannot both succeed.
func practitionerTaken(err error) error {
	var pgErr *pgconn.PgError
	if errors.As(err, &pgErr) && pgErr.ConstraintName == "users_one_practitioner" {
		return ErrPractitionerTaken
	}
	return err
}
