package auth

import "context"

// Role is one of the administrator roles ADR-002 takes from the requirement
// documents. A user may hold several; users.roles holds at least one.
type Role string

const (
	ContentEditor Role = "content_editor"
	BookingAdmin  Role = "booking_admin"
	SiteAdmin     Role = "site_admin"
)

// HasRole reports whether a user holding roles may act as r. site_admin
// implies the other two, so Daw Mi, who holds it, is never refused a route
// another role may use.
func HasRole(roles []string, r Role) bool {
	for _, held := range roles {
		if Role(held) == r || Role(held) == SiteAdmin {
			return true
		}
	}
	return false
}

type sessionKey struct{}

// WithSession returns ctx carrying s, the session a request authenticated
// with.
func WithSession(ctx context.Context, s Session) context.Context {
	return context.WithValue(ctx, sessionKey{}, s)
}

// FromContext is the session the request authenticated with, and false when
// it did not authenticate with one. The HTTP layer sets it for every
// signed-in operation and has checked the operation's roles before any
// handler runs; domain code reads it to apply rules finer than a whole
// route, such as which settings keys a role may change.
func FromContext(ctx context.Context) (Session, bool) {
	s, ok := ctx.Value(sessionKey{}).(Session)
	return s, ok
}
