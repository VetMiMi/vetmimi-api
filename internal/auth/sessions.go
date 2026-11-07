// Package auth signs administrators in and checks their requests. SignIn
// checks the password (argon2id) and, when enrolled, a TOTP code, then starts
// a session; Authenticate turns a session token back into the signed-in user.
package auth

import (
	"context"
	"errors"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/VetMiMi/vetmimi-api/internal/apperr"
	"github.com/VetMiMi/vetmimi-api/internal/clock"
	"github.com/VetMiMi/vetmimi-api/internal/db"
	"github.com/VetMiMi/vetmimi-api/internal/tokens"
)

type User struct {
	ID           pgtype.UUID
	Email        string
	DisplayName  string
	Roles        []string
	Practitioner bool
	TwoStep      bool // signs in with a TOTP code as well as the password
}

type Session struct {
	ID   pgtype.UUID
	User User
}

type Sessions struct {
	pool    *pgxpool.Pool
	codes   *TOTP
	lockout *Lockout
	now     clock.Now
	// dummySecret is a sealed TOTP secret no user has, checked when the user has none.
	dummySecret []byte
}

type sessionKey struct{}

// A session ends after SessionIdle without a request or SessionLifetime in
// all, whichever comes first.
const (
	SessionIdle     = 12 * time.Hour
	SessionLifetime = 7 * 24 * time.Hour
	// lastSeenGrain spares a busy admin page a write on every request.
	lastSeenGrain = time.Minute
)

var errUnauthenticated = apperr.New(apperr.Unauthenticated, "A valid session is required.")

// NewSessions takes a nil lockout to refuse every sign-in, as if Redis were down.
func NewSessions(pool *pgxpool.Pool, codes *TOTP, lockout *Lockout, now clock.Now) (*Sessions, error) {
	dummy, err := codes.Seal(make([]byte, totpSecretBytes))
	if err != nil {
		return nil, err
	}
	sessions := &Sessions{
		pool:        pool,
		codes:       codes,
		lockout:     lockout,
		now:         now,
		dummySecret: dummy,
	}
	return sessions, nil
}

// Authenticate returns the live session token names. Every refusal is the
// same unauthenticated error, so a caller learns nothing about why.
func (sessions *Sessions) Authenticate(ctx context.Context, token string) (Session, error) {
	if !tokens.IsSession(token) {
		return Session{}, errUnauthenticated
	}
	q := db.New(sessions.pool)
	row, err := q.GetSession(ctx, tokens.Hash(token))
	if errors.Is(err, pgx.ErrNoRows) {
		return Session{}, errUnauthenticated
	}
	if err != nil {
		return Session{}, err
	}

	now := sessions.now()
	expired := !now.Before(row.ExpiresAt) || !now.Before(row.LastSeenAt.Add(SessionIdle))
	if expired || row.DisabledAt.Valid {
		return Session{}, errUnauthenticated
	}
	if now.Sub(row.LastSeenAt) > lastSeenGrain {
		err = q.TouchSession(ctx, db.TouchSessionParams{ID: row.ID, Now: now})
		if err != nil {
			return Session{}, err
		}
	}

	session := Session{
		ID: row.ID,
		User: User{
			ID:           row.UserID,
			Email:        row.Email,
			DisplayName:  row.DisplayName,
			Roles:        row.Roles,
			Practitioner: row.IsPractitioner,
			TwoStep:      row.TwoStepEnabled,
		},
	}
	return session, nil
}

func (sessions *Sessions) SignOut(ctx context.Context, id pgtype.UUID) error {
	return db.New(sessions.pool).DeleteSession(ctx, id)
}

// DeleteExpiredSessions only keeps the table small; Authenticate already
// refuses expired sessions.
func DeleteExpiredSessions(ctx context.Context, q db.Querier, now time.Time) (int64, error) {
	return q.DeleteExpiredSessions(ctx, db.DeleteExpiredSessionsParams{
		Now:        now,
		IdleBefore: now.Add(-SessionIdle),
	})
}

func WithSession(ctx context.Context, session Session) context.Context {
	return context.WithValue(ctx, sessionKey{}, session)
}

// FromContext returns the session the HTTP layer authenticated, whose route
// roles it has already checked; domain code uses it for finer rules.
func FromContext(ctx context.Context) (Session, bool) {
	session, ok := ctx.Value(sessionKey{}).(Session)
	return session, ok
}
