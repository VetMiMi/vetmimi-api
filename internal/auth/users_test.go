package auth_test

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"os"
	"testing"

	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgtype"
	"github.com/stretchr/testify/require"

	"github.com/VetMiMi/vetmimi-api/internal/auth"
	"github.com/VetMiMi/vetmimi-api/internal/db"
	"github.com/VetMiMi/vetmimi-api/internal/platform/pgtest"
)

func TestMain(m *testing.M) { os.Exit(pgtest.Run(m)) }

// uniqueEmail keeps tests apart: they share one database.
func uniqueEmail(t *testing.T) string {
	t.Helper()
	b := make([]byte, 6)
	_, err := rand.Read(b)
	require.NoError(t, err)
	return "user-" + hex.EncodeToString(b) + "@example.com"
}

func account(email string) auth.Account {
	return auth.Account{
		Email:            email,
		DisplayName:      "Daw Mi",
		PasswordHash:     hash,
		Roles:            []string{"site_admin"},
		SealedTOTPSecret: []byte("sealed"),
	}
}

func requireViolates(t *testing.T, err error, constraint string) {
	t.Helper()
	var pgErr *pgconn.PgError
	require.True(t, errors.As(err, &pgErr), "got %v", err)
	require.Equal(t, constraint, pgErr.ConstraintName)
}

func insert(q db.Querier, email string, roles []string) error {
	_, err := q.CreateUser(context.Background(), db.CreateUserParams{
		Email: email, DisplayName: "Test", PasswordHash: hash, Roles: roles, TotpSecretEnc: []byte("sealed"),
	})
	return err
}

func TestRolesCheckConstraint(t *testing.T) {
	q := db.New(pgtest.Pool(t))
	requireViolates(t, insert(q, uniqueEmail(t), []string{}), "users_roles_check")
	requireViolates(t, insert(q, uniqueEmail(t), []string{"site_admin", "owner"}), "users_roles_check")
	require.NoError(t, insert(q, uniqueEmail(t), []string{"content_editor", "booking_admin", "site_admin"}))
}

func TestEmailIsUniqueAndLowerCase(t *testing.T) {
	q := db.New(pgtest.Pool(t))
	email := uniqueEmail(t)
	require.NoError(t, insert(q, email, []string{"site_admin"}))
	requireViolates(t, insert(q, email, []string{"site_admin"}), "users_email_key")
	requireViolates(t, insert(q, "Mi-"+email, []string{"site_admin"}), "users_email_lower_case")
}

type savedUser struct {
	Email, DisplayName, PasswordHash string
	Roles                            []string
	IsPractitioner                   bool
	TotpSecretEnc                    []byte
	TotpLastStep                     pgtype.Int8
}

func loadUser(t *testing.T, id pgtype.UUID) savedUser {
	t.Helper()
	var u savedUser
	err := pgtest.Pool(t).QueryRow(context.Background(),
		`SELECT email, display_name, password_hash, roles, is_practitioner, totp_secret_enc, totp_last_step
		 FROM users WHERE id = $1`, id).
		Scan(&u.Email, &u.DisplayName, &u.PasswordHash, &u.Roles, &u.IsPractitioner, &u.TotpSecretEnc, &u.TotpLastStep)
	require.NoError(t, err)
	return u
}

func TestSaveAccountCreatesThenReplaces(t *testing.T) {
	pool := pgtest.Pool(t)
	q := db.New(pool)
	ctx := context.Background()
	email := uniqueEmail(t)

	first := account(" " + email[:4] + "MIXED" + email[4:] + " ")
	id, created, err := auth.SaveAccount(ctx, q, first)
	require.NoError(t, err)
	require.True(t, created)
	require.Equal(t, email[:4]+"mixed"+email[4:], loadUser(t, id).Email, "stored trimmed and lower-case")

	_, err = pool.Exec(ctx, "UPDATE users SET totp_last_step = 42 WHERE id = $1", id)
	require.NoError(t, err)

	second := auth.Account{
		Email:            first.Email,
		DisplayName:      "Mi",
		PasswordHash:     "$argon2id$replaced",
		Roles:            []string{"content_editor", "booking_admin"},
		SealedTOTPSecret: []byte("resealed"),
		EnrolmentStep:    7,
	}
	again, created, err := auth.SaveAccount(ctx, q, second)
	require.NoError(t, err)
	require.False(t, created)
	require.Equal(t, id, again)

	got := loadUser(t, id)
	require.Equal(t, "Mi", got.DisplayName)
	require.Equal(t, "$argon2id$replaced", got.PasswordHash)
	require.Equal(t, []string{"content_editor", "booking_admin"}, got.Roles)
	require.Equal(t, []byte("resealed"), got.TotpSecretEnc)
	require.Equal(t, pgtype.Int8{Int64: 7, Valid: true}, got.TotpLastStep, "the old secret's last step gives way to the enrolment step")
}

func TestSecondPractitionerRefused(t *testing.T) {
	pool := pgtest.Pool(t)
	q := db.New(pool)
	ctx := context.Background()
	// Other tests in this binary may want the one practitioner slot.
	t.Cleanup(func() {
		_, err := pool.Exec(ctx, "DELETE FROM users WHERE is_practitioner")
		require.NoError(t, err)
	})

	mi := account(uniqueEmail(t))
	mi.Practitioner = true
	miID, _, err := auth.SaveAccount(ctx, q, mi)
	require.NoError(t, err)

	other := account(uniqueEmail(t))
	other.Practitioner = true
	_, _, err = auth.SaveAccount(ctx, q, other)
	require.ErrorIs(t, err, auth.ErrPractitionerTaken, "a new second practitioner")

	other.Practitioner = false
	_, _, err = auth.SaveAccount(ctx, q, other)
	require.NoError(t, err)
	other.Practitioner = true
	_, _, err = auth.SaveAccount(ctx, q, other)
	require.ErrorIs(t, err, auth.ErrPractitionerTaken, "an existing user made a second practitioner")

	mi.Practitioner = false
	_, created, err := auth.SaveAccount(ctx, q, mi)
	require.NoError(t, err)
	require.False(t, created)
	require.True(t, loadUser(t, miID).IsPractitioner, "re-enrolling without --practitioner keeps her the practitioner")
}
