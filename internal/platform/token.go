package platform

import (
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"strings"
)

// A session token is sessionTokenPrefix and 32 random bytes in unpadded
// base64url, 43 characters: the SessionCreated.token pattern in
// openapi.yaml. The prefix makes a leaked token recognisable in a scanner or
// a paste.
const (
	sessionTokenPrefix = "vms_"
	sessionTokenBytes  = 32
	sessionTokenChars  = 43
)

// NewSessionToken returns a new random session token.
func NewSessionToken() (string, error) {
	b := make([]byte, sessionTokenBytes)
	if _, err := rand.Read(b); err != nil {
		return "", err
	}
	return sessionTokenPrefix + base64.RawURLEncoding.EncodeToString(b), nil
}

// IsSessionToken reports whether s has the form NewSessionToken returns, so
// anything else is refused without a database lookup.
func IsSessionToken(s string) bool {
	body, ok := strings.CutPrefix(s, sessionTokenPrefix)
	if !ok || len(body) != sessionTokenChars {
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

// HashToken is the SHA-256 a token is stored and looked up by. A token has
// 256 random bits, so a plain hash is enough: there is nothing to guess.
func HashToken(token string) []byte {
	sum := sha256.Sum256([]byte(token))
	return sum[:]
}

// NewManagementToken derives the token in a visitor's management link from
// its seed (docs/architecture.md, "Token formats"): 43 characters of
// base64url HMAC-SHA256 under SIGNING_SECRET, so the database alone holds no
// usable link. Booking stores its hash; the worker derives it again to put
// the link in an email.
func NewManagementToken(secret, seed []byte) string {
	return linkToken(secret, "manage", seed)
}

// NewJoinToken derives a video room's join token from its seed in the same
// way, under its own label, so one seed can never open the other link.
func NewJoinToken(secret, seed []byte) string {
	return linkToken(secret, "join", seed)
}

func linkToken(secret []byte, label string, seed []byte) string {
	mac := hmac.New(sha256.New, secret)
	mac.Write([]byte(label))
	mac.Write(seed)
	return base64.RawURLEncoding.EncodeToString(mac.Sum(nil))
}
