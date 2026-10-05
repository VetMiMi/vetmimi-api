// Package auth owns administrator accounts: password hashing, TOTP enrolment
// and verification, and (as they arrive) sessions and roles.
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

// The argon2id parameters from docs/architecture.md. 64 MiB per hash is the
// most the 2 GB live host can spare during a sign-in.
const (
	argonMemoryKiB = 64 * 1024
	argonTime      = 3
	argonThreads   = 2
	argonSaltBytes = 16
	argonKeyBytes  = 32
)

// argonMinKeyBytes refuses a stored hash too short to mean anything: an
// empty key compares equal to any password's empty key.
const argonMinKeyBytes = 16

var errMalformedHash = errors.New("auth: malformed password hash")

// b64 is the PHC string encoding: standard base64 without padding.
var b64 = base64.RawStdEncoding

// HashPassword returns password hashed with argon2id as a PHC string,
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

// VerifyPassword reports whether password matches the PHC string encoded. It
// hashes with the parameters stored in encoded, not the current constants,
// so raising them later does not lock anyone out. It returns an error only
// when encoded cannot be read.
func VerifyPassword(password, encoded string) (bool, error) {
	parts := strings.Split(encoded, "$")
	if len(parts) != 6 || parts[0] != "" || parts[1] != "argon2id" {
		return false, errMalformedHash
	}
	var version int
	if _, err := fmt.Sscanf(parts[2], "v=%d", &version); err != nil || version != argon2.Version {
		return false, errMalformedHash
	}
	var memory, rounds uint32
	var threads uint8
	if _, err := fmt.Sscanf(parts[3], "m=%d,t=%d,p=%d", &memory, &rounds, &threads); err != nil {
		return false, errMalformedHash
	}
	// argon2.IDKey panics on zero rounds or zero threads.
	if rounds == 0 || threads == 0 {
		return false, errMalformedHash
	}
	salt, err := b64.DecodeString(parts[4])
	if err != nil || len(salt) == 0 {
		return false, errMalformedHash
	}
	want, err := b64.DecodeString(parts[5])
	if err != nil || len(want) < argonMinKeyBytes {
		return false, errMalformedHash
	}
	got := argon2.IDKey([]byte(password), salt, rounds, memory, threads, uint32(len(want)))
	return subtle.ConstantTimeCompare(got, want) == 1, nil
}
