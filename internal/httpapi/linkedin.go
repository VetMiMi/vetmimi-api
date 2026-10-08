package httpapi

import (
	"context"

	"github.com/VetMiMi/vetmimi-api/internal/httpapi/gen"
	"github.com/VetMiMi/vetmimi-api/internal/linkedin"
	"github.com/VetMiMi/vetmimi-api/internal/platform/apperr"
)

var errNoLinkedIn = apperr.New(apperr.Unavailable, "The LinkedIn connector is not wired.")

// GetLinkedInConnection reads the LinkedIn profile connection.
func (s *server) GetLinkedInConnection(ctx context.Context, _ gen.GetLinkedInConnectionRequestObject) (gen.GetLinkedInConnectionResponseObject, error) {
	if s.LinkedIn == nil {
		return nil, errNoLinkedIn
	}
	c, err := s.LinkedIn.Status(ctx, s.Now())
	if err != nil {
		return nil, err
	}
	return gen.GetLinkedInConnection200JSONResponse(linkedInConnectionView(c)), nil
}

// StartLinkedInConnection returns the LinkedIn sign-in address for the
// signed-in administrator.
func (s *server) StartLinkedInConnection(ctx context.Context, _ gen.StartLinkedInConnectionRequestObject) (gen.StartLinkedInConnectionResponseObject, error) {
	if s.LinkedIn == nil {
		return nil, errNoLinkedIn
	}
	link, err := s.LinkedIn.AuthorizeURL(actor(ctx), s.Now())
	if err != nil {
		return nil, err
	}
	return gen.StartLinkedInConnection200JSONResponse{AuthorizeUrl: link}, nil
}

// FinishLinkedInConnection completes the LinkedIn sign-in.
func (s *server) FinishLinkedInConnection(ctx context.Context, req gen.FinishLinkedInConnectionRequestObject) (gen.FinishLinkedInConnectionResponseObject, error) {
	if s.LinkedIn == nil {
		return nil, errNoLinkedIn
	}
	c, err := s.LinkedIn.Finish(ctx, actor(ctx), req.Body.Code, req.Body.State, s.Now())
	if err != nil {
		return nil, err
	}
	s.Log.InfoContext(ctx, "linkedin_connected", "request_id", RequestID(ctx), "status", c.Status)
	return gen.FinishLinkedInConnection200JSONResponse(linkedInConnectionView(c)), nil
}

// DisconnectLinkedIn deletes the connection and its token.
func (s *server) DisconnectLinkedIn(ctx context.Context, _ gen.DisconnectLinkedInRequestObject) (gen.DisconnectLinkedInResponseObject, error) {
	if s.LinkedIn == nil {
		return nil, errNoLinkedIn
	}
	if err := s.LinkedIn.Disconnect(ctx); err != nil {
		return nil, err
	}
	s.Log.InfoContext(ctx, "linkedin_disconnected", "request_id", RequestID(ctx))
	return gen.DisconnectLinkedIn204Response{}, nil
}

func linkedInConnectionView(c linkedin.Connection) gen.LinkedInConnection {
	return gen.LinkedInConnection{
		Status:      gen.LinkedInConnectionStatus(c.Status),
		MemberName:  nonEmpty(c.MemberName),
		ExpiresAt:   optionalTime(c.ExpiresAt.Time, c.ExpiresAt.Valid),
		ConnectedAt: optionalTime(c.ConnectedAt, !c.ConnectedAt.IsZero()),
	}
}
