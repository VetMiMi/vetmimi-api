package auth

import (
	"crypto/rand"
	"crypto/subtle"
	"encoding/base64"
	"errors"
	"fmt"
	"strings"

	"golang.org/x/crypto/argon2"
)

// 64 MiB per hash is the most the 2 GB live host can spare during a sign-in.
const (
	argonMemoryKiB = 64 * 1024
	argonTime      = 3
	argonThreads   = 2
	argonSaltBytes = 16
	argonKeyBytes  = 32
	// argonMinKeyBytes refuses a stored key too short to mean anything: an
	// empty key would match any password.
	argonMinKeyBytes = 16
)

var errMalformedHash = errors.New("auth: malformed password hash")

// b64 is the PHC string encoding: standard base64 without padding.
var b64 = base64.RawStdEncoding

// HashPassword returns an argon2id PHC string,
// $argon2id$v=19$m=65536,t=3,p=2$<salt>$<key>.
func HashPassword(password string) (string, error) {
	salt := make([]byte, argonSaltBytes)
	if _, err := rand.Read(salt); err != nil {
		return "", err
	}
	key := argon2.IDKey([]byte(password), salt, argonTime, argonMemoryKiB, argonThreads, argonKeyBytes)
	return fmt.Sprintf("$argon2id$v=%d$m=%d,t=%d,p=%d$%s$%s",
		argon2.Version, argonMemoryKiB, argonTime, argonThreads, b64.EncodeToString(salt), b64.EncodeToString(key)), nil
}

// VerifyPassword hashes with the parameters stored in encoded, so raising the
// constants later locks no one out. It errors only when encoded is unreadable.
func VerifyPassword(password, encoded string) (bool, error) {
	h, err := parseHash(encoded)
	if err != nil {
		return false, err
	}
	got := argon2.IDKey([]byte(password), h.salt, h.rounds, h.memory, h.threads, uint32(len(h.key)))
	return subtle.ConstantTimeCompare(got, h.key) == 1, nil
}

type passwordHash struct {
	memory, rounds uint32
	threads        uint8
	salt, key      []byte
}

func parseHash(encoded string) (passwordHash, error) {
	var h passwordHash
	parts := strings.Split(encoded, "$")
	if len(parts) != 6 || parts[0] != "" || parts[1] != "argon2id" {
		return h, errMalformedHash
	}
	var version int
	if _, err := fmt.Sscanf(parts[2], "v=%d", &version); err != nil || version != argon2.Version {
		return h, errMalformedHash
	}
	if _, err := fmt.Sscanf(parts[3], "m=%d,t=%d,p=%d", &h.memory, &h.rounds, &h.threads); err != nil {
		return h, errMalformedHash
	}
	// argon2.IDKey panics on zero rounds or zero threads.
	if h.rounds == 0 || h.threads == 0 {
		return h, errMalformedHash
	}
	var err error
	h.salt, err = b64.DecodeString(parts[4])
	if err != nil || len(h.salt) == 0 {
		return h, errMalformedHash
	}
	h.key, err = b64.DecodeString(parts[5])
	if err != nil || len(h.key) < argonMinKeyBytes {
		return h, errMalformedHash
	}
	return h, nil
}
