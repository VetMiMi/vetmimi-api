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

var ErrPractitionerTaken = errors.New("another user is already the practitioner; there can be only one")

// Account is what create-user saves for one administrator.
type Account struct {
	Email            string
	DisplayName      string
	PasswordHash     string
	Roles            []string
	Practitioner     bool
	SealedTOTPSecret []byte // nil for a password-only account
	// EnrolmentStep is saved as the last accepted step, so the code typed at
	// enrolment cannot be replayed to sign in.
	EnrolmentStep int64
}

// NormalizeEmail is the form emails are stored and looked up in.
func NormalizeEmail(email string) string {
	return strings.ToLower(strings.TrimSpace(email))
}

// SaveAccount creates the account or, when the email exists, replaces its
// credentials, name and roles and signs the user out everywhere, in one
// transaction. It reports whether it created the user.
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
		if errors.Is(err, pgx.ErrNoRows) {
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
		}
		if err != nil {
			return practitionerTaken(err)
		}
		return q.DeleteUserSessions(ctx, id)
	})
	if err != nil {
		return pgtype.UUID{}, false, err
	}
	return id, created, nil
}

// practitionerTaken lets the one-practitioner index decide, not a lookup
// first, so two concurrent saves cannot both succeed.
func practitionerTaken(err error) error {
	var pgErr *pgconn.PgError
	if errors.As(err, &pgErr) && pgErr.ConstraintName == "users_one_practitioner" {
		return ErrPractitionerTaken
	}
	return err
}
