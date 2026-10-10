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

	"github.com/VetMiMi/vetmimi-api/internal/apperr"
	"github.com/VetMiMi/vetmimi-api/internal/db"
	"github.com/VetMiMi/vetmimi-api/internal/idempotency"
	"github.com/VetMiMi/vetmimi-api/internal/listing"
	"github.com/VetMiMi/vetmimi-api/internal/queue"
	"github.com/VetMiMi/vetmimi-api/internal/settings"
	"github.com/VetMiMi/vetmimi-api/internal/tokens"
)

type Enquiry struct {
	IdempotencyKey      string
	Body                []byte // the raw request, hashed with the key
	Name, Email         string
	Organisation        string
	Subject             string
	EnquiryType         string
	Service             string // slug, optional
	Message             string
	Locale              string
	PrivacyAcknowledged bool
}

// EnquiryReceipt's JSON is replayed as stored, so it must match openapi.yaml.
type EnquiryReceipt struct {
	Reference string    `json:"reference"`
	CreatedAt time.Time `json:"createdAt"`
}

// EnquiryCreated holds Tasks to enqueue after commit; a replay has none.
type EnquiryCreated struct {
	ID       pgtype.UUID
	Receipt  EnquiryReceipt
	Replayed bool
	Tasks    []queue.Task
}

var (
	errAckRequired = &apperr.Error{Code: apperr.AcknowledgementRequired,
		Detail: "The privacy notice must be acknowledged.",
		Fields: []apperr.FieldError{{Field: "/privacyAcknowledged", Message: "must be true"}}}
	errNoSuchService = apperr.New(apperr.NotFound, "No service has this slug.")
	errNoSuchEnquiry = apperr.New(apperr.NotFound, "No enquiry has this id.")
)

const referenceAttempts = 5

func CreateEnquiry(ctx context.Context, pool *pgxpool.Pool, e Enquiry, now time.Time) (EnquiryCreated, error) {
	if !e.PrivacyAcknowledged {
		return EnquiryCreated{}, errAckRequired
	}
	var out EnquiryCreated
	err := pgx.BeginFunc(ctx, pool, func(tx pgx.Tx) error {
		q := db.New(tx)
		if e.IdempotencyKey == "" {
			var err error
			out, err = createEnquiry(ctx, q, e, now)
			return err
		}

		stored, err := idempotency.Begin(ctx, q, idempotency.ContactEnquiry, e.IdempotencyKey, e.Body, now)
		if err != nil {
			return err
		}
		if stored != nil {
			out = EnquiryCreated{ID: stored.ResourceID, Replayed: true}
			return json.Unmarshal(stored.Body, &out.Receipt)
		}
		if out, err = createEnquiry(ctx, q, e, now); err != nil {
			return err
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
	s, err := settings.Load(ctx, q)
	if err != nil {
		return EnquiryCreated{}, err
	}
	task, err := Queue(ctx, q, Message{ContactEnquiryID: row.ID, Kind: PractitionerNewEnquiry, Recipient: s.ContactEmail})
	if err != nil {
		return EnquiryCreated{}, err
	}
	return EnquiryCreated{
		ID:      row.ID,
		Receipt: EnquiryReceipt{Reference: row.Reference, CreatedAt: row.CreatedAt.UTC()},
		Tasks:   []queue.Task{task},
	}, nil
}

func insertEnquiry(ctx context.Context, q *db.Queries, p db.InsertContactEnquiryParams) (db.ContactEnquiry, error) {
	for range referenceAttempts {
		var err error
		if p.Reference, err = tokens.NewReference("EN-"); err != nil {
			return db.ContactEnquiry{}, err
		}
		row, err := q.InsertContactEnquiry(ctx, p)
		// ON CONFLICT DO NOTHING returns no row when the reference is taken.
		if !errors.Is(err, pgx.ErrNoRows) {
			return row, err
		}
	}
	return db.ContactEnquiry{}, fmt.Errorf("comms: no free enquiry reference after %d attempts", referenceAttempts)
}

type EnquiryFilter struct {
	Status string
	Search string
	Cursor string
	Limit  int
}

type EnquiryPage struct {
	Items      []db.ListContactEnquiriesRow
	NextCursor string
}

func ListEnquiries(ctx context.Context, q db.Querier, f EnquiryFilter) (EnquiryPage, error) {
	limit := f.Limit
	if limit <= 0 {
		limit = 50
	}
	p := db.ListContactEnquiriesParams{
		Status:  pgtype.Text{String: f.Status, Valid: f.Status != ""},
		Search:  listing.LikePattern(f.Search),
		MaxRows: int32(limit) + 1,
	}
	if f.Cursor != "" {
		var err error
		if p.AfterAt, p.AfterID, err = listing.DecodeCursor(f.Cursor); err != nil {
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
		page.NextCursor = listing.EncodeCursor(last.CreatedAt, last.ID)
	}
	return page, nil
}

func GetEnquiry(ctx context.Context, q db.Querier, id pgtype.UUID) (db.GetContactEnquiryRow, error) {
	row, err := q.GetContactEnquiry(ctx, id)
	if errors.Is(err, pgx.ErrNoRows) {
		return row, errNoSuchEnquiry
	}
	return row, err
}

// MarkHandled keeps the first handled time and handler if called again.
func MarkHandled(ctx context.Context, q db.Querier, id, by pgtype.UUID, now time.Time) (db.GetContactEnquiryRow, error) {
	if _, err := q.MarkContactEnquiryHandled(ctx, db.MarkContactEnquiryHandledParams{ID: id, HandledBy: by, Now: now}); err != nil {
		return db.GetContactEnquiryRow{}, err
	}
	return GetEnquiry(ctx, q, id)
}

var enquiryTypeLabels = map[string]string{
	"collaboration":   "Collaboration / Project",
	"workshop":        "Workshop / Program",
	"speaking":        "Speaking / Event",
	"art_of_wellness": "Art of Wellness",
	"media":           "Media / Interview",
	"organisation":    "Organisation / Healthcare",
	"general":         "General",
}

func (t *Tasks) enquiryData(ctx context.Context, q db.Querier, id pgtype.UUID) (RenderData, error) {
	e, err := q.GetContactEnquiry(ctx, id)
	if err != nil {
		return RenderData{}, err
	}
	d := RenderData{
		VisitorName:         e.Name,
		VisitorEmail:        e.Email,
		EnquiryType:         enquiryTypeLabels[e.EnquiryType],
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
