package httpapi

import (
	"context"
	"errors"
	"log/slog"
	"net/http"
	"strings"

	openapi_types "github.com/oapi-codegen/runtime/types"

	"github.com/VetMiMi/vetmimi-api/internal/auth"
	"github.com/VetMiMi/vetmimi-api/internal/httpapi/gen"
	"github.com/VetMiMi/vetmimi-api/internal/platform/apperr"
)

// requireSession answers 401 unauthenticated to a request for a sessionToken
// operation that does not carry a live session as a bearer token (ADR-002),
// and puts the session in the context of one that does, where auth.FromContext
// finds it; their handlers can rely on it. Other operations are
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
			next.ServeHTTP(w, r.WithContext(auth.WithSession(r.Context(), session)))
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

// CreateSession signs an administrator in. Every refusal of the credentials
// is the same 401 invalid_credentials; an email over its attempt limit or
// locked out is 429 rate_limited, logged with a hash prefix of the email
// only.
func (s *server) CreateSession(ctx context.Context, req gen.CreateSessionRequestObject) (gen.CreateSessionResponseObject, error) {
	email := string(req.Body.Email)
	signedIn, err := s.Sessions.SignIn(ctx, auth.Credentials{
		Email:    email,
		Password: req.Body.Password,
		Code:     req.Body.TotpCode,
	})
	var refused *apperr.Error
	if errors.As(err, &refused) && refused.Code == apperr.RateLimited {
		s.Log.WarnContext(ctx, "sign_in_locked", "request_id", RequestID(ctx), "email_hash", auth.EmailHashPrefix(email))
	}
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
	session, _ := auth.FromContext(ctx)
	if err := s.Sessions.SignOut(ctx, session.ID); err != nil {
		return nil, err
	}
	s.Log.InfoContext(ctx, "signed_out", "request_id", RequestID(ctx), "user_id", session.User.ID.String())
	return gen.DeleteCurrentSession204Response{}, nil
}

// GetCurrentUser answers with the user the session belongs to, as the
// session middleware loaded them.
func (s *server) GetCurrentUser(ctx context.Context, _ gen.GetCurrentUserRequestObject) (gen.GetCurrentUserResponseObject, error) {
	session, _ := auth.FromContext(ctx)
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
