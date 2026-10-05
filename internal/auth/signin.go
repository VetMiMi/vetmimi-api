package auth

import (
	"context"
	"errors"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/VetMiMi/vetmimi-api/internal/db"
	"github.com/VetMiMi/vetmimi-api/internal/platform"
	"github.com/VetMiMi/vetmimi-api/internal/platform/apperr"
)

// dummyPasswordHash is a random password, long forgotten, hashed with the
// production parameters. An unknown email is checked against it, so it costs
// the same argon2 work as a known one and timing does not reveal which
// emails have accounts.
const dummyPasswordHash = "$argon2id$v=19$m=65536,t=3,p=2$LXcBUkL8yDkGdP/u9T4YWA$oE4u/5HRqbHdj/1Brl5k1Eo2jRsAAOWowlpOqpT6lGM"

// verifyPassword is VerifyPassword; tests replace it to see which hashes
// sign-in checks.
var verifyPassword = VerifyPassword

// Credentials are what an administrator types to sign in.
type Credentials struct {
	Email    string
	Password string
	Code     string
}

// SignedIn is a new session. Token is the only copy of the token; the
// database keeps its hash.
type SignedIn struct {
	Token     string
	ExpiresAt time.Time
	Session   Session
}

// SignIn checks the credentials and starts a session.
//
// First the lockout counts the attempt against the email's hourly limit and
// refuses an email that is over it or locked out with rate_limited, whether
// or not it has an account, before any password is hashed. Every refusal
// after that (an unknown email, a wrong password, a wrong, replayed or
// concurrently reused code, a disabled user) is the same invalid_credentials
// error, runs the same password hash and code check, and counts as a failure
// towards the lockout. A success leaves the failures to expire on their own,
// so it does not tell an attacker which guess was right.
func (s *Sessions) SignIn(ctx context.Context, c Credentials) (SignedIn, error) {
	email := NormalizeEmail(c.Email)
	if err := s.lockout.admit(ctx, email); err != nil {
		return SignedIn{}, err
	}
	signedIn, err := s.checkAndStart(ctx, email, c)
	var refused *apperr.Error
	if errors.As(err, &refused) && refused.Code == apperr.InvalidCredentials {
		if err := s.lockout.failed(ctx, email); err != nil {
			return SignedIn{}, err
		}
	}
	return signedIn, err
}

// checkAndStart checks the credentials for email and starts a session.
func (s *Sessions) checkAndStart(ctx context.Context, email string, c Credentials) (SignedIn, error) {
	u, err := db.New(s.pool).GetUserForSignIn(ctx, email)
	known := err == nil
	if err != nil && !errors.Is(err, pgx.ErrNoRows) {
		return SignedIn{}, err
	}
	if !known {
		u.PasswordHash, u.TotpSecretEnc = dummyPasswordHash, s.dummySecret
	}
	passwordOK, err := verifyPassword(c.Password, u.PasswordHash)
	if err != nil {
		return SignedIn{}, err
	}
	step, codeOK, err := s.codes.MatchSealed(u.TotpSecretEnc, c.Code)
	if err != nil {
		return SignedIn{}, err
	}
	if !known || !passwordOK || !codeOK {
		return SignedIn{}, invalidCredentials()
	}
	return s.start(ctx, u, step)
}

// start claims the code's step, records the sign-in and inserts the session
// in one transaction, so a failed insert leaves the code unused. The step
// claim and RecordSignIn lock the user's row: of two sign-ins with one code,
// the second waits, then finds the step taken.
func (s *Sessions) start(ctx context.Context, u db.GetUserForSignInRow, step int64) (SignedIn, error) {
	token, err := platform.NewSessionToken()
	if err != nil {
		return SignedIn{}, err
	}
	now := s.now()
	signedIn := SignedIn{
		Token:     token,
		ExpiresAt: now.Add(SessionLifetime),
		Session: Session{User: User{
			ID:           u.ID,
			Email:        u.Email,
			DisplayName:  u.DisplayName,
			Roles:        u.Roles,
			Practitioner: u.IsPractitioner,
		}},
	}
	err = pgx.BeginFunc(ctx, s.pool, func(tx pgx.Tx) error {
		q := db.New(tx)
		if err := ClaimTOTPStep(ctx, q, u.ID, step); err != nil {
			return err
		}
		recorded, err := q.RecordSignIn(ctx, db.RecordSignInParams{ID: u.ID, Now: now, PasswordHash: u.PasswordHash})
		if err != nil {
			return err
		}
		if recorded != 1 {
			// Disabled, or the password changed since it was checked.
			return invalidCredentials()
		}
		signedIn.Session.ID, err = q.CreateSession(ctx, db.CreateSessionParams{
			UserID:    u.ID,
			TokenHash: platform.HashToken(token),
			Now:       now,
			ExpiresAt: signedIn.ExpiresAt,
		})
		return err
	})
	if err != nil {
		return SignedIn{}, err
	}
	return signedIn, nil
}
