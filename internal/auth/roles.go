package auth

// Role is an administrator role. A user holds at least one.
type Role string

const (
	ContentEditor Role = "content_editor"
	BookingAdmin  Role = "booking_admin"
	SiteAdmin     Role = "site_admin"
)

// Roles lists every role; the users_roles_check constraint lists the same.
var Roles = []string{string(ContentEditor), string(BookingAdmin), string(SiteAdmin)}

// HasRole reports whether a user holding roles may act as r. SiteAdmin may
// act as any role.
func HasRole(roles []string, r Role) bool {
	for _, held := range roles {
		if Role(held) == r || Role(held) == SiteAdmin {
			return true
		}
	}
	return false
}
