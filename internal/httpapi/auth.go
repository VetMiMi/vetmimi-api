package httpapi

import (
	"context"
	"errors"

	"github.com/jackc/pgx/v5/pgtype"
	openapi_types "github.com/oapi-codegen/runtime/types"

	"github.com/VetMiMi/vetmimi-api/internal/apperr"
	"github.com/VetMiMi/vetmimi-api/internal/auth"
	"github.com/VetMiMi/vetmimi-api/internal/httpapi/gen"
)

// CreateSession signs an administrator in. A locked-out email is logged by a
// hash prefix only.
func (s *server) CreateSession(ctx context.Context, req gen.CreateSessionRequestObject) (gen.CreateSessionResponseObject, error) {
	email := string(req.Body.Email)
	signedIn, err := s.Sessions.SignIn(ctx, auth.Credentials{
		Email:    email,
		Password: req.Body.Password,
		Code:     deref(req.Body.TotpCode),
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

func (s *server) DeleteCurrentSession(ctx context.Context, _ gen.DeleteCurrentSessionRequestObject) (gen.DeleteCurrentSessionResponseObject, error) {
	session, _ := auth.FromContext(ctx)
	if err := s.Sessions.SignOut(ctx, session.ID); err != nil {
		return nil, err
	}
	s.Log.InfoContext(ctx, "signed_out", "request_id", RequestID(ctx), "user_id", session.User.ID.String())
	return gen.DeleteCurrentSession204Response{}, nil
}

func (s *server) GetCurrentUser(ctx context.Context, _ gen.GetCurrentUserRequestObject) (gen.GetCurrentUserResponseObject, error) {
	session, _ := auth.FromContext(ctx)
	return gen.GetCurrentUser200JSONResponse(currentUser(session.User)), nil
}

// actor is the signed-in administrator; requireSession has already run.
func actor(ctx context.Context) pgtype.UUID {
	session, _ := auth.FromContext(ctx)
	return session.User.ID
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
		TwoStepEnabled: u.TwoStep,
	}
}
