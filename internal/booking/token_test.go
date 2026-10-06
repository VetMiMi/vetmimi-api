package booking_test

import (
	"bytes"
	"regexp"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/VetMiMi/vetmimi-api/internal/booking"
)

var testSecret = bytes.Repeat([]byte("s"), 32)

func TestManagementTokenIsDerivedFromItsSeed(t *testing.T) {
	seed, err := booking.NewSeed()
	require.NoError(t, err)
	require.Len(t, seed, 32)

	token := booking.NewManagementToken(testSecret, seed)
	require.Regexp(t, `^[A-Za-z0-9_-]{43}$`, token)
	require.Equal(t, token, booking.NewManagementToken(testSecret, seed), "same seed, same token")

	other, err := booking.NewSeed()
	require.NoError(t, err)
	require.NotEqual(t, token, booking.NewManagementToken(testSecret, other))
	require.NotEqual(t, token, booking.NewManagementToken(bytes.Repeat([]byte("t"), 32), seed),
		"the seed alone, as stored, gives no usable token")
}

var referencePattern = regexp.MustCompile(`^VM-[0-9ABCDEFGHJKMNPQRSTVWXYZ]{6}$`)

func TestReferenceIsVMAndSixCrockfordCharacters(t *testing.T) {
	seen := map[string]bool{}
	for range 200 {
		ref, err := booking.NewReference()
		require.NoError(t, err)
		require.Regexp(t, referencePattern, ref)
		seen[ref] = true
	}
	require.Greater(t, len(seen), 195, "references are random")
}
