package httpapi

import (
	"context"
	"log/slog"
	"net/http"
	"strings"

	openapi_types "github.com/oapi-codegen/runtime/types"

	"github.com/VetMiMi/vetmimi-api/internal/auth"
	"github.com/VetMiMi/vetmimi-api/internal/httpapi/gen"
	"github.com/VetMiMi/vetmimi-api/internal/platform/apperr"
)

type sessionKey struct{}

// currentSession is the session a signed-in request authenticated with. It
// is set for every sessionToken operation, so their handlers can rely on it.
func currentSession(ctx context.Context) (auth.Session, bool) {
	s, ok := ctx.Value(sessionKey{}).(auth.Session)
	return s, ok
}

// requireSession answers 401 unauthenticated to a request for a sessionToken
// operation that does not carry a live session as a bearer token (ADR-002),
// and puts the session in the context of one that does. Other operations are
// left to their own check. The token is never logged.
func requireSession(sessions *auth.Sessions, ops operations, log *slog.Logger) gen.MiddlewareFunc {
	fail := responseError(log)
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			if op, _ := ops.lookup(r); op.Security != schemeSessionToken {
				next.ServeHTTP(w, r)
				return
			}
			if sessions == nil {
				// A router built without sessions accepts none, rather than
				// failing on the first signed-in call.
				writeProblem(w, apperr.New(apperr.Unauthenticated, "A valid session is required."))
				return
			}
			session, err := sessions.Authenticate(r.Context(), bearerToken(r))
			if err != nil {
				fail(w, r, err)
				return
			}
			next.ServeHTTP(w, r.WithContext(context.WithValue(r.Context(), sessionKey{}, session)))
		})
	}
}

// bearerToken is the token in an "Authorization: Bearer <token>" header, or
// "" without one. The scheme is case-insensitive (RFC 9110, section 11.1).
func bearerToken(r *http.Request) string {
	scheme, token, ok := strings.Cut(r.Header.Get("Authorization"), " ")
	if !ok || !strings.EqualFold(scheme, "Bearer") {
		return ""
	}
	return token
}

// CreateSession signs an administrator in. Every refusal is the same
// 401 invalid_credentials.
func (s *server) CreateSession(ctx context.Context, req gen.CreateSessionRequestObject) (gen.CreateSessionResponseObject, error) {
	signedIn, err := s.Sessions.SignIn(ctx, auth.Credentials{
		Email:    string(req.Body.Email),
		Password: req.Body.Password,
		Code:     req.Body.TotpCode,
	})
	if err != nil {
		return nil, err
	}
	s.Log.InfoContext(ctx, "signed_in", "request_id", RequestID(ctx), "user_id", signedIn.Session.User.ID.String())
	return gen.CreateSession201JSONResponse{
		Token:     signedIn.Token,
		ExpiresAt: signedIn.ExpiresAt.UTC(),
		User:      currentUser(signedIn.Session.User),
	}, nil
}

// DeleteCurrentSession signs out: the session's token stops working at once.
func (s *server) DeleteCurrentSession(ctx context.Context, _ gen.DeleteCurrentSessionRequestObject) (gen.DeleteCurrentSessionResponseObject, error) {
	session, _ := currentSession(ctx)
	if err := s.Sessions.SignOut(ctx, session.ID); err != nil {
		return nil, err
	}
	s.Log.InfoContext(ctx, "signed_out", "request_id", RequestID(ctx), "user_id", session.User.ID.String())
	return gen.DeleteCurrentSession204Response{}, nil
}

// GetCurrentUser answers with the user the session belongs to, as the
// session middleware loaded them.
func (s *server) GetCurrentUser(ctx context.Context, _ gen.GetCurrentUserRequestObject) (gen.GetCurrentUserResponseObject, error) {
	session, _ := currentSession(ctx)
	return gen.GetCurrentUser200JSONResponse(currentUser(session.User)), nil
}

func currentUser(u auth.User) gen.CurrentUser {
	roles := make([]gen.Role, len(u.Roles))
	for i, r := range u.Roles {
		roles[i] = gen.Role(r)
	}
	return gen.CurrentUser{
		Id:             openapi_types.UUID(u.ID.Bytes),
		Email:          openapi_types.Email(u.Email),
		DisplayName:    u.DisplayName,
		Roles:          roles,
		IsPractitioner: u.Practitioner,
	}
}
