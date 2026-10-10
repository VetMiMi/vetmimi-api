package auth

import (
	"context"
	"errors"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/VetMiMi/vetmimi-api/internal/apperr"
	"github.com/VetMiMi/vetmimi-api/internal/db"
	"github.com/VetMiMi/vetmimi-api/internal/tokens"
)

// dummyPasswordHash has the production parameters, so an unknown email costs
// the same argon2 work as a known one and timing does not reveal which exist.
const dummyPasswordHash = "$argon2id$v=19$m=65536,t=3,p=2$LXcBUkL8yDkGdP/u9T4YWA$oE4u/5HRqbHdj/1Brl5k1Eo2jRsAAOWowlpOqpT6lGM"

// verifyPassword is a variable so tests can see which hashes sign-in checks.
var verifyPassword = VerifyPassword

type Credentials struct {
	Email    string
	Password string
	Code     string
}

// SignedIn holds the only copy of the token; the database keeps its hash.
type SignedIn struct {
	Token     string
	ExpiresAt time.Time
	Session   Session
}

// SignIn checks the credentials and starts a session. A user without a TOTP
// secret signs in with the password alone. Every refusal after the lockout
// is the same invalid_credentials, costs the same work and counts as a failure.
func (s *Sessions) SignIn(ctx context.Context, c Credentials) (SignedIn, error) {
	email := NormalizeEmail(c.Email)
	if err := s.lockout.admit(ctx, email); err != nil {
		return SignedIn{}, err
	}
	signedIn, err := s.checkAndStart(ctx, email, c)
	if isInvalidCredentials(err) {
		if err := s.lockout.failed(ctx, email); err != nil {
			return SignedIn{}, err
		}
	}
	// A success leaves earlier failures to expire, so it does not reveal which guess was right.
	return signedIn, err
}

func (s *Sessions) checkAndStart(ctx context.Context, email string, c Credentials) (SignedIn, error) {
	u, err := db.New(s.pool).GetUserForSignIn(ctx, email)
	known := err == nil
	if !known && !errors.Is(err, pgx.ErrNoRows) {
		return SignedIn{}, err
	}
	if !known {
		u.PasswordHash = dummyPasswordHash
	}
	twoStep := u.TotpSecretEnc != nil
	sealed := u.TotpSecretEnc
	if !twoStep {
		// Check a code anyway, so the work done does not reveal which accounts
		// exist or have TOTP; the answer is not used.
		sealed = s.dummySecret
	}

	passwordOK, err := verifyPassword(c.Password, u.PasswordHash)
	if err != nil {
		return SignedIn{}, err
	}
	step, codeOK, err := s.codes.MatchSealed(sealed, c.Code)
	if err != nil {
		return SignedIn{}, err
	}
	if !known || !passwordOK || (twoStep && !codeOK) {
		return SignedIn{}, invalidCredentials()
	}
	return s.start(ctx, u, twoStep, step)
}

// start claims the code's step, records the sign-in and saves the session in
// one transaction, so a failed save leaves the code unused. The claim locks
// the user's row: of two sign-ins with one code, the second finds it taken.
func (s *Sessions) start(ctx context.Context, u db.GetUserForSignInRow, twoStep bool, step int64) (SignedIn, error) {
	token, err := tokens.NewSession()
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
			TwoStep:      twoStep,
		}},
	}
	err = pgx.BeginFunc(ctx, s.pool, func(tx pgx.Tx) error {
		q := db.New(tx)
		if twoStep {
			if err := ClaimTOTPStep(ctx, q, u.ID, step); err != nil {
				return err
			}
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
			TokenHash: tokens.Hash(token),
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

func invalidCredentials() error { return apperr.New(apperr.InvalidCredentials, "") }

func isInvalidCredentials(err error) bool {
	var appErr *apperr.Error
	return errors.As(err, &appErr) && appErr.Code == apperr.InvalidCredentials
}
