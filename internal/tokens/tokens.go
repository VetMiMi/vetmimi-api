// Package tokens makes the strings the API hands out: random session tokens,
// signed link tokens, booking references and OAuth state.
package tokens

import (
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"strings"
)

// A session token is "vms_" and 32 random bytes in unpadded base64url, the
// SessionCreated.token pattern in openapi.yaml. The prefix makes a leaked
// token recognisable.
const (
	sessionPrefix = "vms_"
	sessionBytes  = 32
	sessionChars  = 43
)

func NewSession() (string, error) {
	b := make([]byte, sessionBytes)
	if _, err := rand.Read(b); err != nil {
		return "", err
	}
	return sessionPrefix + base64.RawURLEncoding.EncodeToString(b), nil
}

// IsSession reports whether s has the shape NewSession returns, so anything
// else is refused without a database lookup.
func IsSession(s string) bool {
	body, ok := strings.CutPrefix(s, sessionPrefix)
	if !ok || len(body) != sessionChars {
		return false
	}
	for _, c := range []byte(body) {
		ok := 'a' <= c && c <= 'z' || 'A' <= c && c <= 'Z' || '0' <= c && c <= '9' || c == '-' || c == '_'
		if !ok {
			return false
		}
	}
	return true
}

// Hash is the SHA-256 a token is stored and looked up by. A token has 256
// random bits, so a plain hash is enough.
func Hash(token string) []byte {
	sum := sha256.Sum256([]byte(token))
	return sum[:]
}

// Management derives the token in a visitor's management link from its seed,
// as HMAC-SHA256 under SIGNING_SECRET, so the database alone holds no usable
// link.
func Management(secret, seed []byte) string {
	return linkToken(secret, "manage", seed)
}

// Join derives a video room's join token the same way, under its own label,
// so one seed never opens the other link.
func Join(secret, seed []byte) string {
	return linkToken(secret, "join", seed)
}

func linkToken(secret []byte, label string, seed []byte) string {
	mac := hmac.New(sha256.New, secret)
	mac.Write([]byte(label))
	mac.Write(seed)
	return base64.RawURLEncoding.EncodeToString(mac.Sum(nil))
}
