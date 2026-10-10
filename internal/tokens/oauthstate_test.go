package tokens

import (
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

// A state is good only for its user, its platform and its lifetime, and
// only as signed.
func TestOAuthState(t *testing.T) {
	secret := []byte("test signing secret, 32 bytes ok")
	now := time.Date(2026, 10, 7, 9, 0, 0, 0, time.UTC)
	s := SignOAuthState(secret, "linkedin", "user-1", now.Add(10*time.Minute))

	require.True(t, CheckOAuthState(secret, "linkedin", s, "user-1", now))
	require.False(t, CheckOAuthState(secret, "linkedin", s, "user-2", now), "another user")
	require.False(t, CheckOAuthState(secret, "meta", s, "user-1", now), "another platform")
	require.False(t, CheckOAuthState(secret, "linkedin", s, "user-1", now.Add(10*time.Minute)), "expired")
	require.False(t, CheckOAuthState(secret, "linkedin", "x"+s, "user-1", now), "tampered")
	require.False(t, CheckOAuthState([]byte("another secret, also 32 bytes ok"), "linkedin", s, "user-1", now))
}
