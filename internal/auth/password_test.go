package auth_test

import (
	"encoding/base64"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/VetMiMi/vetmimi-api/internal/auth"
)

// Each hash takes 64 MiB, so these tests hash as few times as they can and
// never in parallel.

func TestPasswordRoundTrip(t *testing.T) {
	hash, err := auth.HashPassword("correct horse battery")
	require.NoError(t, err)

	parts := strings.Split(hash, "$")
	require.Len(t, parts, 6)
	require.Equal(t, "$argon2id$v=19$m=65536,t=3,p=2$", strings.Join(parts[:4], "$")+"$")
	salt, err := base64.RawStdEncoding.DecodeString(parts[4])
	require.NoError(t, err)
	require.Len(t, salt, 16)
	key, err := base64.RawStdEncoding.DecodeString(parts[5])
	require.NoError(t, err)
	require.Len(t, key, 32)

	ok, err := auth.VerifyPassword("correct horse battery", hash)
	require.NoError(t, err)
	require.True(t, ok)

	ok, err = auth.VerifyPassword("correct horse battery!", hash)
	require.NoError(t, err)
	require.False(t, ok, "a wrong password is refused")
}

func TestHashPasswordSaltsEveryHash(t *testing.T) {
	a, err := auth.HashPassword("correct horse battery")
	require.NoError(t, err)
	b, err := auth.HashPassword("correct horse battery")
	require.NoError(t, err)
	require.NotEqual(t, a, b)
}

// hash is "correct horse battery" hashed with the production parameters.
const hash = "$argon2id$v=19$m=65536,t=3,p=2$c2FsdHNhbHRzYWx0c2FsdA$oLbEshmirStBvAHctylRo6RngkqGfbxRX4w4VwYm24U"

func TestTamperedHashIsRefused(t *testing.T) {
	ok, err := auth.VerifyPassword("correct horse battery", hash)
	require.NoError(t, err)
	require.True(t, ok, "the fixture must verify, or the cases below prove nothing")

	tampered := map[string]string{
		"key byte": strings.Replace(hash, "$oLbE", "$pLbE", 1),
		"salt":     strings.Replace(hash, "$c2Fsd", "$d2Fsd", 1),
		"memory":   strings.Replace(hash, "m=65536", "m=32768", 1),
	}
	for name, encoded := range tampered {
		ok, err := auth.VerifyPassword("correct horse battery", encoded)
		require.NoError(t, err, name)
		require.False(t, ok, name)
	}

	malformed := map[string]string{
		"argon2i":        strings.Replace(hash, "$argon2id$", "$argon2i$", 1),
		"other version":  strings.Replace(hash, "v=19", "v=16", 1),
		"zero rounds":    strings.Replace(hash, "t=3", "t=0", 1),
		"zero threads":   strings.Replace(hash, "p=2", "p=0", 1),
		"no salt":        strings.Replace(hash, "$c2FsdHNhbHRzYWx0c2FsdA$", "$$", 1),
		"empty key":      hash[:strings.LastIndex(hash, "$")+1],
		"short key":      hash[:strings.LastIndex(hash, "$")+1] + "AAAAAAAAAAAAAAAAAAAA",
		"bad base64":     hash + "!",
		"missing params": "$argon2id$v=19$c2FsdHNhbHRzYWx0c2FsdA$oLbEshmirStBvAHctylRo6RngkqGfbxRX4w4VwYm24U",
		"empty":          "",
	}
	for name, encoded := range malformed {
		ok, err := auth.VerifyPassword("correct horse battery", encoded)
		require.Error(t, err, name)
		require.False(t, ok, name)
	}
}
