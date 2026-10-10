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

// A session ends after SessionIdle without a request or SessionLifetime in
// all, whichever comes first.
const (
	SessionIdle     = 12 * time.Hour
	SessionLifetime = 7 * 24 * time.Hour
	// lastSeenGrain spares a busy admin page a write on every request.
	lastSeenGrain = time.Minute
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

// NewSessions takes a nil lockout to refuse every sign-in, as if Redis were down.
func NewSessions(pool *pgxpool.Pool, codes *TOTP, lockout *Lockout, now clock.Now) (*Sessions, error) {
	dummy, err := codes.Seal(make([]byte, totpSecretBytes))
	if err != nil {
		return nil, err
	}
	return &Sessions{pool: pool, codes: codes, lockout: lockout, now: now, dummySecret: dummy}, nil
}

// Authenticate returns the live session token names. Every refusal is the
// same unauthenticated error, so a caller learns nothing about why.
func (s *Sessions) Authenticate(ctx context.Context, token string) (Session, error) {
	if !tokens.IsSession(token) {
		return Session{}, unauthenticated()
	}
	q := db.New(s.pool)
	row, err := q.GetSession(ctx, tokens.Hash(token))
	if errors.Is(err, pgx.ErrNoRows) {
		return Session{}, unauthenticated()
	}
	if err != nil {
		return Session{}, err
	}

	now := s.now()
	expired := !now.Before(row.ExpiresAt) || !now.Before(row.LastSeenAt.Add(SessionIdle))
	if expired || row.DisabledAt.Valid {
		return Session{}, unauthenticated()
	}
	if now.Sub(row.LastSeenAt) > lastSeenGrain {
		if err := q.TouchSession(ctx, db.TouchSessionParams{ID: row.ID, Now: now}); err != nil {
			return Session{}, err
		}
	}
	return Session{ID: row.ID, User: User{
		ID:           row.UserID,
		Email:        row.Email,
		DisplayName:  row.DisplayName,
		Roles:        row.Roles,
		Practitioner: row.IsPractitioner,
		TwoStep:      row.TwoStepEnabled,
	}}, nil
}

func (s *Sessions) SignOut(ctx context.Context, id pgtype.UUID) error {
	return db.New(s.pool).DeleteSession(ctx, id)
}

// DeleteExpiredSessions only keeps the table small; Authenticate already
// refuses expired sessions.
func DeleteExpiredSessions(ctx context.Context, q db.Querier, now time.Time) (int64, error) {
	return q.DeleteExpiredSessions(ctx, db.DeleteExpiredSessionsParams{
		Now:        now,
		IdleBefore: now.Add(-SessionIdle),
	})
}

func unauthenticated() error {
	return apperr.New(apperr.Unauthenticated, "A valid session is required.")
}

type sessionKey struct{}

func WithSession(ctx context.Context, s Session) context.Context {
	return context.WithValue(ctx, sessionKey{}, s)
}

// FromContext returns the session the HTTP layer authenticated, whose route
// roles it has already checked; domain code uses it for finer rules.
func FromContext(ctx context.Context) (Session, bool) {
	s, ok := ctx.Value(sessionKey{}).(Session)
	return s, ok
}
