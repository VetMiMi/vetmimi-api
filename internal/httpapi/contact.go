package httpapi

import (
	"context"
	"encoding/json"
	"net/http"

	openapi_types "github.com/oapi-codegen/runtime/types"

	"github.com/VetMiMi/vetmimi-api/internal/comms"
	"github.com/VetMiMi/vetmimi-api/internal/db"
	"github.com/VetMiMi/vetmimi-api/internal/httpapi/gen"
)

// CreateContactEnquiry stores an enquiry and, once it has committed,
// enqueues Daw Mi's notification. The visitor gets no email of their own
// at launch; the site shows the success state. The log carries the id only.
func (s *server) CreateContactEnquiry(ctx context.Context, req gen.CreateContactEnquiryRequestObject) (gen.CreateContactEnquiryResponseObject, error) {
	b := req.Body
	raw, err := json.Marshal(b)
	if err != nil {
		return nil, err
	}
	e := comms.Enquiry{
		Body:                raw,
		Name:                b.Name,
		Email:               string(b.Email),
		Organisation:        deref(b.Organisation),
		Subject:             deref(b.Subject),
		EnquiryType:         string(b.EnquiryType),
		Service:             deref(b.Service),
		Message:             b.Message,
		Locale:              string(b.Locale),
		PrivacyAcknowledged: bool(b.PrivacyAcknowledged),
	}
	if req.Params.IdempotencyKey != nil {
		e.IdempotencyKey = req.Params.IdempotencyKey.String()
	}
	res, err := comms.CreateEnquiry(ctx, s.Pool, e, s.Now())
	if err != nil {
		return nil, err
	}
	if s.Queue != nil {
		s.Queue.Enqueue(ctx, res.Tasks...)
	}
	s.Log.InfoContext(ctx, "contact enquiry received", "request_id", RequestID(ctx),
		"enquiry_id", res.ID.String(), "replayed", res.Replayed)
	receipt := gen.CreateContactEnquiry201JSONResponse{Reference: res.Receipt.Reference, CreatedAt: res.Receipt.CreatedAt}
	if res.Replayed {
		return replayedEnquiry{receipt}, nil
	}
	return receipt, nil
}

type replayedEnquiry struct {
	gen.CreateContactEnquiry201JSONResponse
}

func (r replayedEnquiry) VisitCreateContactEnquiryResponse(w http.ResponseWriter) error {
	w.Header().Set("Idempotent-Replayed", "true")
	return r.CreateContactEnquiry201JSONResponse.VisitCreateContactEnquiryResponse(w)
}

// ListContactEnquiries lists enquiries newest first.
func (s *server) ListContactEnquiries(ctx context.Context, req gen.ListContactEnquiriesRequestObject) (gen.ListContactEnquiriesResponseObject, error) {
	p := req.Params
	page, err := comms.ListEnquiries(ctx, db.New(s.Pool), comms.EnquiryFilter{
		Status: string(deref(p.Status)),
		Search: deref(p.Q),
		Cursor: deref(p.Cursor),
		Limit:  deref(p.Limit),
	})
	if err != nil {
		return nil, err
	}
	out := gen.ListContactEnquiries200JSONResponse{Items: make([]gen.ContactEnquiry, len(page.Items)),
		NextCursor: nonEmpty(page.NextCursor)}
	for i, r := range page.Items {
		out.Items[i] = enquiryView(db.GetContactEnquiryRow(r))
	}
	return out, nil
}

// GetContactEnquiry shows one enquiry in full.
func (s *server) GetContactEnquiry(ctx context.Context, req gen.GetContactEnquiryRequestObject) (gen.GetContactEnquiryResponseObject, error) {
	e, err := comms.GetEnquiry(ctx, db.New(s.Pool), uuid(req.EnquiryId))
	if err != nil {
		return nil, err
	}
	return gen.GetContactEnquiry200JSONResponse(enquiryView(e)), nil
}

// MarkContactEnquiryHandled marks an enquiry handled; a second call changes
// nothing.
func (s *server) MarkContactEnquiryHandled(ctx context.Context, req gen.MarkContactEnquiryHandledRequestObject) (gen.MarkContactEnquiryHandledResponseObject, error) {
	e, err := comms.MarkHandled(ctx, db.New(s.Pool), uuid(req.EnquiryId), actor(ctx), s.Now())
	if err != nil {
		return nil, err
	}
	return gen.MarkContactEnquiryHandled200JSONResponse(enquiryView(e)), nil
}

func enquiryView(e db.GetContactEnquiryRow) gen.ContactEnquiry {
	v := gen.ContactEnquiry{
		Id:           openapi_types.UUID(e.ID.Bytes),
		Reference:    e.Reference,
		Name:         e.Name,
		Email:        openapi_types.Email(e.Email),
		Organisation: optionalString(e.Organisation),
		EnquiryType:  gen.EnquiryType(e.EnquiryType),
		Subject:      optionalString(e.Subject),
		Message:      e.Message,
		Locale:       gen.Locale(e.Locale),
		Status:       gen.ContactEnquiryStatus(e.Status),
		HandledAt:    optionalTime(e.HandledAt.Time, e.HandledAt.Valid),
		CreatedAt:    e.CreatedAt.UTC(),
	}
	if e.ServiceID.Valid {
		v.Service = new(serviceRef(e.ServiceID, e.ServiceSlug.String, e.ServiceName))
	}
	if e.HandledBy.Valid {
		v.HandledBy = new(openapi_types.UUID(e.HandledBy.Bytes))
	}
	return v
}
