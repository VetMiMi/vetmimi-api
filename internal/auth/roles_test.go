package auth_test

import (
	"context"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/VetMiMi/vetmimi-api/internal/auth"
)

var allRoles = []auth.Role{auth.ContentEditor, auth.BookingAdmin, auth.SiteAdmin}

func TestSiteAdminImpliesOtherRoles(t *testing.T) {
	for _, r := range allRoles {
		require.True(t, auth.HasRole([]string{"site_admin"}, r), r)
	}
}

// Content editors never act as booking administrators, nor the other way
// round, and neither acts as a site administrator.
func TestOtherRolesImplyOnlyThemselves(t *testing.T) {
	for _, held := range []auth.Role{auth.ContentEditor, auth.BookingAdmin} {
		for _, r := range allRoles {
			require.Equal(t, r == held, auth.HasRole([]string{string(held)}, r), "%s as %s", held, r)
		}
	}
	both := []string{"content_editor", "booking_admin"}
	require.True(t, auth.HasRole(both, auth.ContentEditor))
	require.True(t, auth.HasRole(both, auth.BookingAdmin))
	require.False(t, auth.HasRole(both, auth.SiteAdmin))
}

func TestNoRolesOrUnknownRolesGrantNothing(t *testing.T) {
	for _, roles := range [][]string{nil, {}, {"admin"}, {"Site_Admin"}, {""}} {
		for _, r := range allRoles {
			require.False(t, auth.HasRole(roles, r), "%q as %s", roles, r)
		}
	}
}

func TestSessionTravelsInTheContext(t *testing.T) {
	_, ok := auth.FromContext(context.Background())
	require.False(t, ok)

	s := auth.Session{User: auth.User{Email: "mi@example.com", Roles: []string{"booking_admin"}}}
	got, ok := auth.FromContext(auth.WithSession(context.Background(), s))
	require.True(t, ok)
	require.Equal(t, s, got)
}
