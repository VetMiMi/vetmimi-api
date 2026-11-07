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

// dummyPasswordHash has the production parameters, so an unknown email costs
// the same argon2 work as a known one and timing does not reveal which exist.
const dummyPasswordHash = "$argon2id$v=19$m=65536,t=3,p=2$LXcBUkL8yDkGdP/u9T4YWA$oE4u/5HRqbHdj/1Brl5k1Eo2jRsAAOWowlpOqpT6lGM"

// verifyPassword is a variable so tests can see which hashes sign-in checks.
var verifyPassword = VerifyPassword

var errInvalidCredentials = apperr.New(apperr.InvalidCredentials, "")

// SignIn checks the credentials and starts a session. A user without a TOTP
// secret signs in with the password alone. Every refusal after the lockout
// is the same invalid_credentials, costs the same work and counts as a failure.
func (sessions *Sessions) SignIn(ctx context.Context, credentials Credentials) (SignedIn, error) {
	email := NormalizeEmail(credentials.Email)
	err := sessions.lockout.admit(ctx, email)
	if err != nil {
		return SignedIn{}, err
	}

	signedIn, err := sessions.checkAndStart(ctx, email, credentials)
	if isInvalidCredentials(err) {
		lockoutErr := sessions.lockout.failed(ctx, email)
		if lockoutErr != nil {
			return SignedIn{}, lockoutErr
		}
	}
	// A success leaves earlier failures to expire, so it does not reveal which guess was right.
	return signedIn, err
}

func (sessions *Sessions) checkAndStart(ctx context.Context, email string, credentials Credentials) (SignedIn, error) {
	user, err := db.New(sessions.pool).GetUserForSignIn(ctx, email)
	known := err == nil
	if !known && !errors.Is(err, pgx.ErrNoRows) {
		return SignedIn{}, err
	}
	if !known {
		user.PasswordHash = dummyPasswordHash
	}
	twoStep := user.TotpSecretEnc != nil
	sealed := user.TotpSecretEnc
	if !twoStep {
		// Check a code anyway, so the work done does not reveal which accounts
		// exist or have TOTP; the answer is not used.
		sealed = sessions.dummySecret
	}

	passwordOK, err := verifyPassword(credentials.Password, user.PasswordHash)
	if err != nil {
		return SignedIn{}, err
	}
	step, codeOK, err := sessions.codes.MatchSealed(sealed, credentials.Code)
	if err != nil {
		return SignedIn{}, err
	}
	if !known || !passwordOK || (twoStep && !codeOK) {
		return SignedIn{}, errInvalidCredentials
	}
	return sessions.start(ctx, user, twoStep, step)
}

// start claims the code's step, records the sign-in and saves the session in
// one transaction, so a failed save leaves the code unused. The claim locks
// the user's row: of two sign-ins with one code, the second finds it taken.
func (sessions *Sessions) start(ctx context.Context, user db.GetUserForSignInRow, twoStep bool, step int64) (SignedIn, error) {
	token, err := tokens.NewSession()
	if err != nil {
		return SignedIn{}, err
	}
	now := sessions.now()
	signedIn := SignedIn{
		Token:     token,
		ExpiresAt: now.Add(SessionLifetime),
		Session: Session{
			User: User{
				ID:           user.ID,
				Email:        user.Email,
				DisplayName:  user.DisplayName,
				Roles:        user.Roles,
				Practitioner: user.IsPractitioner,
				TwoStep:      twoStep,
			},
		},
	}

	tx, err := sessions.pool.Begin(ctx)
	if err != nil {
		return SignedIn{}, err
	}
	defer tx.Rollback(ctx)
	q := db.New(tx)

	if twoStep {
		err = ClaimTOTPStep(ctx, q, user.ID, step)
		if err != nil {
			return SignedIn{}, err
		}
	}
	recorded, err := q.RecordSignIn(ctx, db.RecordSignInParams{
		ID:           user.ID,
		Now:          now,
		PasswordHash: user.PasswordHash,
	})
	if err != nil {
		return SignedIn{}, err
	}
	if recorded != 1 {
		// Disabled, or the password changed since it was checked.
		return SignedIn{}, errInvalidCredentials
	}
	sessionID, err := q.CreateSession(ctx, db.CreateSessionParams{
		UserID:    user.ID,
		TokenHash: tokens.Hash(token),
		Now:       now,
		ExpiresAt: signedIn.ExpiresAt,
	})
	if err != nil {
		return SignedIn{}, err
	}
	signedIn.Session.ID = sessionID

	err = tx.Commit(ctx)
	if err != nil {
		return SignedIn{}, err
	}
	return signedIn, nil
}

func isInvalidCredentials(err error) bool {
	var appErr *apperr.Error
	return errors.As(err, &appErr) && appErr.Code == apperr.InvalidCredentials
}
