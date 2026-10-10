package secretbox_test

import (
	"bytes"
	"encoding/hex"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/VetMiMi/vetmimi-api/internal/secretbox"
)

var testKey = []byte("0123456789abcdef0123456789abcdef")

func newBox(t *testing.T, key []byte) *secretbox.Box {
	t.Helper()
	box, err := secretbox.New(key)
	require.NoError(t, err)
	return box
}

func TestSealedValueOpensOnlyUnderItsKey(t *testing.T) {
	box := newBox(t, testKey)
	plain := []byte("page-token-secret")

	sealed, err := box.Seal(plain)
	require.NoError(t, err)
	require.Len(t, sealed, 12+len(plain)+16, "nonce, plaintext, GCM tag")
	again, err := box.Seal(plain)
	require.NoError(t, err)
	require.NotEqual(t, sealed[:12], again[:12], "every seal has a fresh nonce")

	opened, err := box.Open(sealed)
	require.NoError(t, err)
	require.Equal(t, plain, opened)

	_, err = newBox(t, bytes.Repeat([]byte{8}, 32)).Open(sealed)
	require.Error(t, err, "another key")

	tampered := bytes.Clone(sealed)
	tampered[len(tampered)-1] ^= 1
	_, err = box.Open(tampered)
	require.Error(t, err, "altered ciphertext")

	_, err = box.Open(sealed[:5])
	require.Error(t, err, "shorter than a nonce")
}

// Sealed by auth.TOTP before this package existed: stored tokens must still open.
func TestValueSealedBeforeTheExtractionStillOpens(t *testing.T) {
	sealed, err := hex.DecodeString("b4acdb9cf4a11a1bb4699fb1907c21f5b383867ac2d68be0ea635e2b3ceb92cf7c60aa30a86b5384b25888d92b")
	require.NoError(t, err)

	opened, err := newBox(t, testKey).Open(sealed)
	require.NoError(t, err)
	require.Equal(t, "page-token-secret", string(opened))
}

func TestNewNeedsAnAES256Key(t *testing.T) {
	for _, n := range []int{0, 16, 31, 33} {
		_, err := secretbox.New(make([]byte, n))
		require.Error(t, err, n)
	}
}
