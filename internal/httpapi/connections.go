package httpapi

import (
	"context"

	"github.com/VetMiMi/vetmimi-api/internal/apperr"
	"github.com/VetMiMi/vetmimi-api/internal/httpapi/gen"
	"github.com/VetMiMi/vetmimi-api/internal/linkedin"
	"github.com/VetMiMi/vetmimi-api/internal/meta"
)

var (
	errNoMeta     = apperr.New(apperr.Unavailable, "The Meta connector is not wired.")
	errNoLinkedIn = apperr.New(apperr.Unavailable, "The LinkedIn connector is not wired.")
)

func (s *server) GetMetaConnection(ctx context.Context, _ gen.GetMetaConnectionRequestObject) (gen.GetMetaConnectionResponseObject, error) {
	if s.Meta == nil {
		return nil, errNoMeta
	}
	c, err := s.Meta.Status(ctx)
	if err != nil {
		return nil, err
	}
	return gen.GetMetaConnection200JSONResponse(metaConnectionView(c)), nil
}

// StartMetaConnection returns the Facebook Login address.
func (s *server) StartMetaConnection(ctx context.Context, _ gen.StartMetaConnectionRequestObject) (gen.StartMetaConnectionResponseObject, error) {
	if s.Meta == nil {
		return nil, errNoMeta
	}
	link, err := s.Meta.AuthorizeURL(actor(ctx), s.Now())
	if err != nil {
		return nil, err
	}
	return gen.StartMetaConnection200JSONResponse{AuthorizeUrl: link}, nil
}

func (s *server) FinishMetaConnection(ctx context.Context, req gen.FinishMetaConnectionRequestObject) (gen.FinishMetaConnectionResponseObject, error) {
	if s.Meta == nil {
		return nil, errNoMeta
	}
	c, err := s.Meta.Finish(ctx, actor(ctx), req.Body.Code, req.Body.State, s.Now())
	if err != nil {
		return nil, err
	}
	s.Log.InfoContext(ctx, "meta_signed_in", "request_id", RequestID(ctx), "status", c.Status)
	return gen.FinishMetaConnection200JSONResponse(metaConnectionView(c)), nil
}

func (s *server) ChooseMetaPage(ctx context.Context, req gen.ChooseMetaPageRequestObject) (gen.ChooseMetaPageResponseObject, error) {
	if s.Meta == nil {
		return nil, errNoMeta
	}
	c, err := s.Meta.ChoosePage(ctx, actor(ctx), req.Body.PageId, s.Now())
	if err != nil {
		return nil, err
	}
	s.Log.InfoContext(ctx, "meta_connected", "request_id", RequestID(ctx), "page_id", c.PageID)
	return gen.ChooseMetaPage200JSONResponse(metaConnectionView(c)), nil
}

func (s *server) DisconnectMeta(ctx context.Context, _ gen.DisconnectMetaRequestObject) (gen.DisconnectMetaResponseObject, error) {
	if s.Meta == nil {
		return nil, errNoMeta
	}
	if err := s.Meta.Disconnect(ctx); err != nil {
		return nil, err
	}
	s.Log.InfoContext(ctx, "meta_disconnected", "request_id", RequestID(ctx))
	return gen.DisconnectMeta204Response{}, nil
}

func metaConnectionView(c meta.Connection) gen.MetaConnection {
	v := gen.MetaConnection{
		Status:            gen.MetaConnectionStatus(c.Status),
		PageId:            nonEmpty(c.PageID),
		PageName:          nonEmpty(c.PageName),
		InstagramId:       nonEmpty(c.InstagramID),
		InstagramUsername: nonEmpty(c.InstagramUsername),
		LastError:         nonEmpty(c.LastError),
		ExpiresAt:         optionalTime(c.ExpiresAt.Time, c.ExpiresAt.Valid),
		ConnectedAt:       optionalTime(c.ConnectedAt, !c.ConnectedAt.IsZero()),
	}
	if c.Status == "choosing_page" {
		pages := make([]gen.MetaPage, len(c.Pages))
		for i, p := range c.Pages {
			pages[i] = gen.MetaPage{Id: p.ID, Name: p.Name}
			if p.Instagram != nil {
				pages[i].InstagramUsername = nonEmpty(p.Instagram.Username)
			}
		}
		v.Pages = &pages
	}
	return v
}

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

// StartLinkedInConnection returns the LinkedIn sign-in address.
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
