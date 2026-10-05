package platform

import (
	"encoding/base64"
	"encoding/hex"
	"regexp"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
)

// The SessionCreated.token pattern in openapi.yaml.
var sessionTokenPattern = regexp.MustCompile(`^vms_[A-Za-z0-9_-]{43}$`)

func TestSessionTokensMatchTheContract(t *testing.T) {
	seen := map[string]bool{}
	for range 100 {
		token, err := NewSessionToken()
		require.NoError(t, err)
		require.Regexp(t, sessionTokenPattern, token)
		require.True(t, IsSessionToken(token))
		raw, err := base64.RawURLEncoding.DecodeString(strings.TrimPrefix(token, "vms_"))
		require.NoError(t, err)
		require.Len(t, raw, 32)
		require.False(t, seen[token])
		seen[token] = true
	}
}

func TestIsSessionTokenRefusesOtherShapes(t *testing.T) {
	good := "vms_" + strings.Repeat("aB3-_", 8) + "xyz"
	require.True(t, IsSessionToken(good))
	for name, s := range map[string]string{
		"empty":           "",
		"prefix only":     "vms_",
		"no prefix":       strings.TrimPrefix(good, "vms_"),
		"other prefix":    "vmx_" + good[4:],
		"one short":       good[:len(good)-1],
		"one long":        good + "a",
		"padding":         good[:len(good)-1] + "=",
		"standard base64": good[:len(good)-1] + "+",
		"newline":         good[:len(good)-1] + "\n",
		"space":           good[:len(good)-1] + " ",
		"non-ASCII":       good[:len(good)-2] + "é",
	} {
		require.False(t, IsSessionToken(s), name)
		require.False(t, sessionTokenPattern.MatchString(s), "%s: the contract refuses it too", name)
	}
}

func TestHashTokenIsSHA256(t *testing.T) {
	require.Equal(t, "ba7816bf8f01cfea414140de5dae2223b00361a396177a9cb410ff61f20015ad",
		hex.EncodeToString(HashToken("abc")))
}
