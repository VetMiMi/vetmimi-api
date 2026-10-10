package auth

import (
	"context"
	"errors"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/VetMiMi/vetmimi-api/internal/db"
	"github.com/VetMiMi/vetmimi-api/internal/platform"
	"github.com/VetMiMi/vetmimi-api/internal/platform/apperr"
	"github.com/VetMiMi/vetmimi-api/internal/platform/clock"
)

// Session lifetimes from ADR-002 and docs/architecture.md: a session ends
// after twelve idle hours or seven days in all, whichever comes first.
const (
	SessionIdle     = 12 * time.Hour
	SessionLifetime = 7 * 24 * time.Hour
	// lastSeenGrain is how stale last_seen_at may be before a request
	// refreshes it, so a busy admin page writes once a minute, not on every
	// request. It is far below SessionIdle, so the idle expiry moves by a
	// minute at most.
	lastSeenGrain = time.Minute
)

// User is the signed-in administrator.
type User struct {
	ID           pgtype.UUID
	Email        string
	DisplayName  string
	Roles        []string
	Practitioner bool
	// TwoStep is whether the user signs in with a TOTP code as well as the
	// password.
	TwoStep bool
}

// Session is the live session a request authenticated with.
type Session struct {
	ID   pgtype.UUID
	User User
}

// Sessions signs administrators in and out and authenticates their requests.
type Sessions struct {
	pool    *pgxpool.Pool
	codes   *TOTP
	lockout *Lockout
	now     clock.Now
	// dummySecret is a sealed TOTP secret no user has. Sign-in checks the code
	// of an unknown email against it, so every attempt does the same work.
	dummySecret []byte
}

// NewSessions returns Sessions backed by pool, checking codes with codes,
// limiting sign-in attempts with lockout and reading the time from now. A
// nil lockout refuses every sign-in, as if Redis were down.
func NewSessions(pool *pgxpool.Pool, codes *TOTP, lockout *Lockout, now clock.Now) (*Sessions, error) {
	dummy, err := codes.Seal(make([]byte, totpSecretBytes))
	if err != nil {
		return nil, err
	}
	return &Sessions{pool: pool, codes: codes, lockout: lockout, now: now, dummySecret: dummy}, nil
}

// Authenticate returns the live session token names. A token that is
// malformed, unknown, idle for SessionIdle, past its absolute expiry or
// belonging to a disabled user is refused with the same unauthenticated
// error, so a caller learns nothing about which. A live session's
// last_seen_at moves to now when it is more than lastSeenGrain old.
func (s *Sessions) Authenticate(ctx context.Context, token string) (Session, error) {
	if !platform.IsSessionToken(token) {
		return Session{}, unauthenticated()
	}
	q := db.New(s.pool)
	row, err := q.GetSession(ctx, platform.HashToken(token))
	if errors.Is(err, pgx.ErrNoRows) {
		return Session{}, unauthenticated()
	}
	if err != nil {
		return Session{}, err
	}
	now := s.now()
	if !now.Before(row.ExpiresAt) {
		return Session{}, unauthenticated()
	}
	if !now.Before(row.LastSeenAt.Add(SessionIdle)) {
		return Session{}, unauthenticated()
	}
	if row.DisabledAt.Valid {
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

// SignOut deletes the session, so its token stops working at once.
func (s *Sessions) SignOut(ctx context.Context, id pgtype.UUID) error {
	return db.New(s.pool).DeleteSession(ctx, id)
}

// DeleteExpiredSessions deletes every session past either expiry at now and
// reports how many. Authenticate already refuses them; this only keeps the
// table small.
func DeleteExpiredSessions(ctx context.Context, q db.Querier, now time.Time) (int64, error) {
	return q.DeleteExpiredSessions(ctx, db.DeleteExpiredSessionsParams{
		Now:        now,
		IdleBefore: now.Add(-SessionIdle),
	})
}

func unauthenticated() error {
	return apperr.New(apperr.Unauthenticated, "A valid session is required.")
}
