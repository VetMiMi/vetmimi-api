package comms

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/VetMiMi/vetmimi-api/internal/db"
	"github.com/VetMiMi/vetmimi-api/internal/platform"
	"github.com/VetMiMi/vetmimi-api/internal/platform/apperr"
	"github.com/VetMiMi/vetmimi-api/internal/platform/idempotency"
	"github.com/VetMiMi/vetmimi-api/internal/platform/settings"
)

// Enquiry is a message from the contact form or an enquiry-only service.
type Enquiry struct {
	// IdempotencyKey is optional; without one every submission is new.
	IdempotencyKey string
	// Body is the request as the handler received it; the key's hash covers it.
	Body                []byte
	Name, Email         string
	Organisation        string
	Subject             string
	EnquiryType         string
	Service             string // slug, optional
	Message             string
	Locale              string
	PrivacyAcknowledged bool
}

// EnquiryReceipt is what the visitor is told. Its JSON is the stored
// idempotent response, so it matches ContactEnquiryReceipt.
type EnquiryReceipt struct {
	Reference string    `json:"reference"`
	CreatedAt time.Time `json:"createdAt"`
}

// EnquiryCreated is the outcome of CreateEnquiry. Tasks are to be enqueued
// after it returns; a replay has none.
type EnquiryCreated struct {
	ID       pgtype.UUID
	Receipt  EnquiryReceipt
	Replayed bool
	Tasks    []platform.Task
}

var (
	errAckRequired = &apperr.Error{Code: apperr.AcknowledgementRequired,
		Detail: "The privacy notice must be acknowledged.",
		Fields: []apperr.FieldError{{Field: "/privacyAcknowledged", Message: "must be true"}}}
	errNoSuchService = apperr.New(apperr.NotFound, "No service has this slug.")
	errNoSuchEnquiry = apperr.New(apperr.NotFound, "No enquiry has this id.")
)

// referenceAttempts bounds the retries on a reference collision.
const referenceAttempts = 5

// CreateEnquiry stores an enquiry and queues Daw Mi's notification in one
// transaction. There is no reply to the visitor at launch: the requirements
// name only her notification, and the site shows the success state.
func CreateEnquiry(ctx context.Context, pool *pgxpool.Pool, e Enquiry, now time.Time) (EnquiryCreated, error) {
	if !e.PrivacyAcknowledged {
		return EnquiryCreated{}, errAckRequired
	}
	var out EnquiryCreated
	err := pgx.BeginFunc(ctx, pool, func(tx pgx.Tx) error {
		q := db.New(tx)
		if e.IdempotencyKey != "" {
			stored, err := idempotency.Begin(ctx, q, idempotency.ContactEnquiry, e.IdempotencyKey, e.Body, now)
			if err != nil {
				return err
			}
			if stored != nil {
				out = EnquiryCreated{ID: stored.ResourceID, Replayed: true}
				return json.Unmarshal(stored.Body, &out.Receipt)
			}
		}
		var err error
		if out, err = createEnquiry(ctx, q, e, now); err != nil {
			return err
		}
		if e.IdempotencyKey == "" {
			return nil
		}
		return idempotency.Finish(ctx, q, idempotency.ContactEnquiry, e.IdempotencyKey, out.ID, 201, out.Receipt)
	})
	return out, err
}

func createEnquiry(ctx context.Context, q *db.Queries, e Enquiry, now time.Time) (EnquiryCreated, error) {
	p := db.InsertContactEnquiryParams{
		Name:         strings.TrimSpace(e.Name),
		Email:        strings.ToLower(strings.TrimSpace(e.Email)),
		Organisation: optional(e.Organisation),
		Subject:      optional(e.Subject),
		EnquiryType:  e.EnquiryType,
		Message:      strings.TrimSpace(e.Message),
		Locale:       e.Locale,
		PrivacyAckAt: now,
	}
	if e.Service != "" {
		svc, err := q.GetServiceBySlug(ctx, e.Service)
		if errors.Is(err, pgx.ErrNoRows) {
			return EnquiryCreated{}, errNoSuchService
		}
		if err != nil {
			return EnquiryCreated{}, err
		}
		p.ServiceID = svc.ID
	}
	row, err := insertEnquiry(ctx, q, p)
	if err != nil {
		return EnquiryCreated{}, err
	}
	cur, err := settings.Load(ctx, q)
	if err != nil {
		return EnquiryCreated{}, err
	}
	task, err := Queue(ctx, q, Message{ContactEnquiryID: row.ID, Kind: PractitionerNewEnquiry, Recipient: cur.ContactEmail})
	if err != nil {
		return EnquiryCreated{}, err
	}
	return EnquiryCreated{
		ID:      row.ID,
		Receipt: EnquiryReceipt{Reference: row.Reference, CreatedAt: row.CreatedAt.UTC()},
		Tasks:   []platform.Task{task},
	}, nil
}

func insertEnquiry(ctx context.Context, q *db.Queries, p db.InsertContactEnquiryParams) (db.ContactEnquiry, error) {
	for range referenceAttempts {
		var err error
		if p.Reference, err = platform.NewReference("EN-"); err != nil {
			return db.ContactEnquiry{}, err
		}
		row, err := q.InsertContactEnquiry(ctx, p)
		if !errors.Is(err, pgx.ErrNoRows) {
			return row, err
		}
	}
	return db.ContactEnquiry{}, fmt.Errorf("comms: no free enquiry reference after %d attempts", referenceAttempts)
}

// EnquiryFilter narrows the admin list; zero fields filter nothing.
type EnquiryFilter struct {
	Status string
	Search string
	Cursor string
	Limit  int
}

// EnquiryPage is one page of the list, newest first. NextCursor is empty on
// the last page.
type EnquiryPage struct {
	Items      []db.ListContactEnquiriesRow
	NextCursor string
}

// ListEnquiries lists enquiries for the admin, a page at a time.
func ListEnquiries(ctx context.Context, q db.Querier, f EnquiryFilter) (EnquiryPage, error) {
	limit := f.Limit
	if limit <= 0 {
		limit = 50
	}
	p := db.ListContactEnquiriesParams{
		Status:  pgtype.Text{String: f.Status, Valid: f.Status != ""},
		Search:  platform.LikePattern(f.Search),
		MaxRows: int32(limit) + 1,
	}
	if f.Cursor != "" {
		var err error
		if p.AfterAt, p.AfterID, err = platform.DecodeCursor(f.Cursor); err != nil {
			return EnquiryPage{}, err
		}
	}
	rows, err := q.ListContactEnquiries(ctx, p)
	if err != nil {
		return EnquiryPage{}, err
	}
	page := EnquiryPage{Items: rows}
	if len(rows) > limit {
		page.Items = rows[:limit]
		last := page.Items[limit-1]
		page.NextCursor = platform.EncodeCursor(last.CreatedAt, last.ID)
	}
	return page, nil
}

// GetEnquiry reads one enquiry.
func GetEnquiry(ctx context.Context, q db.Querier, id pgtype.UUID) (db.GetContactEnquiryRow, error) {
	row, err := q.GetContactEnquiry(ctx, id)
	if errors.Is(err, pgx.ErrNoRows) {
		return row, errNoSuchEnquiry
	}
	return row, err
}

// MarkHandled records that Daw Mi has dealt with an enquiry. An enquiry
// already handled keeps its first handled time and handler.
func MarkHandled(ctx context.Context, q db.Querier, id, by pgtype.UUID, now time.Time) (db.GetContactEnquiryRow, error) {
	if _, err := q.MarkContactEnquiryHandled(ctx, db.MarkContactEnquiryHandledParams{ID: id, HandledBy: by, Now: now}); err != nil {
		return db.GetContactEnquiryRow{}, err
	}
	return GetEnquiry(ctx, q, id)
}

// enquiryTypes are the admin's labels for enquiry_type (vetmimi-next#77).
var enquiryTypes = map[string]string{
	"collaboration":   "Collaboration / Project",
	"workshop":        "Workshop / Program",
	"speaking":        "Speaking / Event",
	"art_of_wellness": "Art of Wellness",
	"media":           "Media / Interview",
	"organisation":    "Organisation / Healthcare",
	"general":         "General",
}

// enquiryData is what Daw Mi's notification shows about an enquiry.
func (t *Tasks) enquiryData(ctx context.Context, q db.Querier, id pgtype.UUID) (RenderData, error) {
	e, err := q.GetContactEnquiry(ctx, id)
	if err != nil {
		return RenderData{}, err
	}
	d := RenderData{
		VisitorName:         e.Name,
		VisitorEmail:        e.Email,
		EnquiryType:         enquiryTypes[e.EnquiryType],
		EnquirySubject:      e.Subject.String,
		EnquiryMessage:      e.Message,
		EnquiryOrganisation: e.Organisation.String,
		AdminURL:            strings.TrimRight(t.SiteURL, "/") + "/admin/enquiries/" + e.ID.String(),
	}
	if d.EnquirySubject == "" {
		d.EnquirySubject = d.EnquiryType
	}
	return d, nil
}

func optional(s string) pgtype.Text {
	s = strings.TrimSpace(s)
	return pgtype.Text{String: s, Valid: s != ""}
}
