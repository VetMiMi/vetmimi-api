package booking

import (
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
)

const seedBytes = 32

// NewSeed returns the random seed a management link is derived from. The
// seed is stored, so the worker can render the link; the token never is, only
// its platform.HashToken.
func NewSeed() ([]byte, error) {
	seed := make([]byte, seedBytes)
	if _, err := rand.Read(seed); err != nil {
		return nil, err
	}
	return seed, nil
}

// NewManagementToken derives the token in a visitor's management link from
// its seed (docs/architecture.md, "Token formats"): 43 characters of
// base64url HMAC-SHA256 under SIGNING_SECRET, so the database alone holds no
// usable link.
func NewManagementToken(secret, seed []byte) string {
	mac := hmac.New(sha256.New, secret)
	mac.Write([]byte("manage"))
	mac.Write(seed)
	return base64.RawURLEncoding.EncodeToString(mac.Sum(nil))
}
