package auth

import (
	"context"
	"crypto/aes"
	"crypto/cipher"
	"crypto/rand"
	"crypto/subtle"
	"encoding/base32"
	"errors"
	"time"

	"github.com/jackc/pgx/v5/pgtype"
	"github.com/pquerna/otp"
	"github.com/pquerna/otp/totp"

	"github.com/VetMiMi/vetmimi-api/internal/clock"
	"github.com/VetMiMi/vetmimi-api/internal/db"
)

// Settings every authenticator app understands: a 20-byte secret, HMAC-SHA-1,
// six digits, a new code every 30 seconds.
const (
	totpIssuer      = "VetMiMi"
	totpPeriod      = 30
	totpSecretBytes = 20
	totpKeyBytes    = 32 // AES-256
)

var totpCodeOpts = totp.ValidateOpts{Period: totpPeriod, Digits: otp.DigitsSix, Algorithm: otp.AlgorithmSHA1}

var b32 = base32.StdEncoding.WithPadding(base32.NoPadding)

// TOTP checks codes against secrets and encrypts the secrets at rest.
type TOTP struct {
	aead cipher.AEAD
	now  clock.Now
}

// NewTOTP takes TOTP_ENCRYPTION_KEY, which must be 32 bytes.
func NewTOTP(key []byte, now clock.Now) (*TOTP, error) {
	if len(key) != totpKeyBytes {
		return nil, errors.New("auth: the TOTP encryption key must be 32 bytes")
	}
	block, err := aes.NewCipher(key)
	if err != nil {
		return nil, err
	}
	aead, err := cipher.NewGCM(block)
	if err != nil {
		return nil, err
	}
	return &TOTP{aead: aead, now: now}, nil
}

// Enrolment is a new secret and the otpauth:// URI that carries it to an
// authenticator app. Both are secret.
type Enrolment struct {
	Secret []byte
	URI    string
}

func NewEnrolment(email string) (Enrolment, error) {
	key, err := totp.Generate(totp.GenerateOpts{
		Issuer:      totpIssuer,
		AccountName: email,
		Period:      totpPeriod,
		SecretSize:  totpSecretBytes,
		Digits:      otp.DigitsSix,
		Algorithm:   otp.AlgorithmSHA1,
	})
	if err != nil {
		return Enrolment{}, err
	}
	secret, err := b32.DecodeString(key.Secret())
	if err != nil {
		return Enrolment{}, err
	}
	return Enrolment{Secret: secret, URI: key.URL()}, nil
}

// Seal encrypts with AES-256-GCM and prepends the random nonce.
func (t *TOTP) Seal(secret []byte) ([]byte, error) {
	nonce := make([]byte, t.aead.NonceSize())
	if _, err := rand.Read(nonce); err != nil {
		return nil, err
	}
	return t.aead.Seal(nonce, nonce, secret, nil), nil
}

// Open fails if sealed was altered or sealed under another key.
func (t *TOTP) Open(sealed []byte) ([]byte, error) {
	n := t.aead.NonceSize()
	if len(sealed) < n {
		return nil, errors.New("auth: sealed TOTP secret is too short")
	}
	return t.aead.Open(nil, sealed[:n], sealed[n:], nil)
}

// Match returns the step whose code equals code: the current step, or the one
// either side of it to allow for a phone's clock drifting.
func (t *TOTP) Match(secret []byte, code string) (step int64, ok bool) {
	current := t.now().Unix() / totpPeriod
	for _, s := range []int64{current, current - 1, current + 1} {
		if subtle.ConstantTimeCompare([]byte(codeAt(secret, s)), []byte(code)) == 1 {
			return s, true
		}
	}
	return 0, false
}

func codeAt(secret []byte, step int64) string {
	// The only error is a secret that is not base32, which b32 cannot produce.
	code, _ := totp.GenerateCodeCustom(b32.EncodeToString(secret), time.Unix(step*totpPeriod, 0), totpCodeOpts)
	return code
}

func (t *TOTP) MatchSealed(sealed []byte, code string) (step int64, ok bool, err error) {
	secret, err := t.Open(sealed)
	if err != nil {
		return 0, false, err
	}
	step, ok = t.Match(secret, code)
	return step, ok, nil
}

// ClaimTOTPStep accepts step only if it is later than the user's last
// accepted one, so a replayed or concurrently reused code is refused with
// invalid_credentials.
func ClaimTOTPStep(ctx context.Context, q db.Querier, userID pgtype.UUID, step int64) error {
	claimed, err := q.ClaimTOTPStep(ctx, db.ClaimTOTPStepParams{ID: userID, Step: step})
	if err != nil {
		return err
	}
	if claimed != 1 {
		return invalidCredentials()
	}
	return nil
}
