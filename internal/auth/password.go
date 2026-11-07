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

type passwordHash struct {
	memory  uint32
	rounds  uint32
	threads uint8
	salt    []byte
	key     []byte
}

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

// b64 is the PHC string encoding: standard base64 without padding.
var b64 = base64.RawStdEncoding

var errMalformedHash = errors.New("auth: malformed password hash")

// HashPassword returns an argon2id PHC string,
// $argon2id$v=19$m=65536,t=3,p=2$<salt>$<key>.
func HashPassword(password string) (string, error) {
	salt := make([]byte, argonSaltBytes)
	_, err := rand.Read(salt)
	if err != nil {
		return "", err
	}
	key := argon2.IDKey([]byte(password), salt, argonTime, argonMemoryKiB, argonThreads, argonKeyBytes)
	encoded := fmt.Sprintf("$argon2id$v=%d$m=%d,t=%d,p=%d$%s$%s",
		argon2.Version, argonMemoryKiB, argonTime, argonThreads, b64.EncodeToString(salt), b64.EncodeToString(key))
	return encoded, nil
}

// VerifyPassword hashes with the parameters stored in encoded, so raising the
// constants later locks no one out. It errors only when encoded is unreadable.
func VerifyPassword(password, encoded string) (bool, error) {
	hash, err := parseHash(encoded)
	if err != nil {
		return false, err
	}
	got := argon2.IDKey([]byte(password), hash.salt, hash.rounds, hash.memory, hash.threads, uint32(len(hash.key)))
	return subtle.ConstantTimeCompare(got, hash.key) == 1, nil
}

func parseHash(encoded string) (passwordHash, error) {
	var hash passwordHash
	parts := strings.Split(encoded, "$")
	if len(parts) != 6 || parts[0] != "" || parts[1] != "argon2id" {
		return hash, errMalformedHash
	}
	var version int
	_, err := fmt.Sscanf(parts[2], "v=%d", &version)
	if err != nil || version != argon2.Version {
		return hash, errMalformedHash
	}
	_, err = fmt.Sscanf(parts[3], "m=%d,t=%d,p=%d", &hash.memory, &hash.rounds, &hash.threads)
	if err != nil {
		return hash, errMalformedHash
	}
	// argon2.IDKey panics on zero rounds or zero threads.
	if hash.rounds == 0 || hash.threads == 0 {
		return hash, errMalformedHash
	}
	hash.salt, err = b64.DecodeString(parts[4])
	if err != nil || len(hash.salt) == 0 {
		return hash, errMalformedHash
	}
	hash.key, err = b64.DecodeString(parts[5])
	if err != nil || len(hash.key) < argonMinKeyBytes {
		return hash, errMalformedHash
	}
	return hash, nil
}
