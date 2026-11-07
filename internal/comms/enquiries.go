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
	Name                string
	Email               string
	Organisation        string
	Subject             string
	EnquiryType         string
	Service             string // slug, optional
	Message             string
	Locale              string
	PrivacyAcknowledged bool
}

// EnquiryReceipt's JSON is replayed as stored, so its shape must not change.
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

const referenceAttempts = 5

var enquiryTypeLabels = map[string]string{
	"collaboration":   "Collaboration / Project",
	"workshop":        "Workshop / Program",
	"speaking":        "Speaking / Event",
	"art_of_wellness": "Art of Wellness",
	"media":           "Media / Interview",
	"organisation":    "Organisation / Healthcare",
	"general":         "General",
}

var (
	errAckRequired = &apperr.Error{
		Code:   apperr.AcknowledgementRequired,
		Detail: "The privacy notice must be acknowledged.",
		Fields: []apperr.FieldError{{Field: "/privacyAcknowledged", Message: "must be true"}},
	}
	errNoSuchService = apperr.New(apperr.NotFound, "No service has this slug.")
	errNoSuchEnquiry = apperr.New(apperr.NotFound, "No enquiry has this id.")
)

func CreateEnquiry(ctx context.Context, pool *pgxpool.Pool, enquiry Enquiry, now time.Time) (EnquiryCreated, error) {
	if !enquiry.PrivacyAcknowledged {
		return EnquiryCreated{}, errAckRequired
	}

	tx, err := pool.Begin(ctx)
	if err != nil {
		return EnquiryCreated{}, err
	}
	defer tx.Rollback(ctx)
	q := db.New(tx)

	created, err := createOrReplay(ctx, q, enquiry, now)
	if err != nil {
		return EnquiryCreated{}, err
	}

	err = tx.Commit(ctx)
	if err != nil {
		return EnquiryCreated{}, err
	}
	return created, nil
}

func ListEnquiries(ctx context.Context, q db.Querier, filter EnquiryFilter) (EnquiryPage, error) {
	limit := filter.Limit
	if limit <= 0 {
		limit = 50
	}
	params := db.ListContactEnquiriesParams{
		Status:  pgtype.Text{String: filter.Status, Valid: filter.Status != ""},
		Search:  listing.LikePattern(filter.Search),
		MaxRows: int32(limit) + 1,
	}
	if filter.Cursor != "" {
		afterAt, afterID, err := listing.DecodeCursor(filter.Cursor)
		if err != nil {
			return EnquiryPage{}, err
		}
		params.AfterAt = afterAt
		params.AfterID = afterID
	}

	rows, err := q.ListContactEnquiries(ctx, params)
	if err != nil {
		return EnquiryPage{}, err
	}
	if len(rows) <= limit {
		return EnquiryPage{Items: rows}, nil
	}
	last := rows[limit-1]
	page := EnquiryPage{
		Items:      rows[:limit],
		NextCursor: listing.EncodeCursor(last.CreatedAt, last.ID),
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
	_, err := q.MarkContactEnquiryHandled(ctx, db.MarkContactEnquiryHandledParams{
		ID:        id,
		HandledBy: by,
		Now:       now,
	})
	if err != nil {
		return db.GetContactEnquiryRow{}, err
	}
	return GetEnquiry(ctx, q, id)
}

// createOrReplay stores the enquiry once per idempotency key; a key seen before replays its receipt.
func createOrReplay(ctx context.Context, q *db.Queries, enquiry Enquiry, now time.Time) (EnquiryCreated, error) {
	if enquiry.IdempotencyKey == "" {
		return storeEnquiry(ctx, q, enquiry, now)
	}

	stored, err := idempotency.Begin(ctx, q, idempotency.ContactEnquiry, enquiry.IdempotencyKey, enquiry.Body, now)
	if err != nil {
		return EnquiryCreated{}, err
	}
	if stored != nil {
		replay := EnquiryCreated{ID: stored.ResourceID, Replayed: true}
		err = json.Unmarshal(stored.Body, &replay.Receipt)
		if err != nil {
			return EnquiryCreated{}, err
		}
		return replay, nil
	}
	created, err := storeEnquiry(ctx, q, enquiry, now)
	if err != nil {
		return EnquiryCreated{}, err
	}
	err = idempotency.Finish(ctx, q, idempotency.ContactEnquiry, enquiry.IdempotencyKey, created.ID, 201, created.Receipt)
	if err != nil {
		return EnquiryCreated{}, err
	}
	return created, nil
}

// storeEnquiry inserts the enquiry and Daw Mi's email row.
func storeEnquiry(ctx context.Context, q *db.Queries, enquiry Enquiry, now time.Time) (EnquiryCreated, error) {
	params := db.InsertContactEnquiryParams{
		Name:         strings.TrimSpace(enquiry.Name),
		Email:        strings.ToLower(strings.TrimSpace(enquiry.Email)),
		Organisation: optional(enquiry.Organisation),
		Subject:      optional(enquiry.Subject),
		EnquiryType:  enquiry.EnquiryType,
		Message:      strings.TrimSpace(enquiry.Message),
		Locale:       enquiry.Locale,
		PrivacyAckAt: now,
	}
	if enquiry.Service != "" {
		service, err := q.GetServiceBySlug(ctx, enquiry.Service)
		if errors.Is(err, pgx.ErrNoRows) {
			return EnquiryCreated{}, errNoSuchService
		}
		if err != nil {
			return EnquiryCreated{}, err
		}
		params.ServiceID = service.ID
	}
	row, err := insertEnquiry(ctx, q, params)
	if err != nil {
		return EnquiryCreated{}, err
	}
	current, err := settings.Load(ctx, q)
	if err != nil {
		return EnquiryCreated{}, err
	}
	task, err := Queue(ctx, q, Message{
		ContactEnquiryID: row.ID,
		Kind:             PractitionerNewEnquiry,
		Recipient:        current.ContactEmail,
	})
	if err != nil {
		return EnquiryCreated{}, err
	}
	created := EnquiryCreated{
		ID:      row.ID,
		Receipt: EnquiryReceipt{Reference: row.Reference, CreatedAt: row.CreatedAt.UTC()},
		Tasks:   []queue.Task{task},
	}
	return created, nil
}

func insertEnquiry(ctx context.Context, q *db.Queries, params db.InsertContactEnquiryParams) (db.ContactEnquiry, error) {
	for range referenceAttempts {
		reference, err := tokens.NewReference("EN-")
		if err != nil {
			return db.ContactEnquiry{}, err
		}
		params.Reference = reference
		row, err := q.InsertContactEnquiry(ctx, params)
		// ON CONFLICT DO NOTHING returns no row when the reference is taken.
		if !errors.Is(err, pgx.ErrNoRows) {
			return row, err
		}
	}
	return db.ContactEnquiry{}, fmt.Errorf("comms: no free enquiry reference after %d attempts", referenceAttempts)
}

func (t *Tasks) enquiryData(ctx context.Context, q db.Querier, id pgtype.UUID) (RenderData, error) {
	enquiry, err := q.GetContactEnquiry(ctx, id)
	if err != nil {
		return RenderData{}, err
	}
	data := RenderData{
		VisitorName:         enquiry.Name,
		VisitorEmail:        enquiry.Email,
		EnquiryType:         enquiryTypeLabels[enquiry.EnquiryType],
		EnquirySubject:      enquiry.Subject.String,
		EnquiryMessage:      enquiry.Message,
		EnquiryOrganisation: enquiry.Organisation.String,
		AdminURL:            strings.TrimRight(t.SiteURL, "/") + "/admin/enquiries/" + enquiry.ID.String(),
	}
	if data.EnquirySubject == "" {
		data.EnquirySubject = data.EnquiryType
	}
	return data, nil
}

func optional(text string) pgtype.Text {
	text = strings.TrimSpace(text)
	return pgtype.Text{String: text, Valid: text != ""}
}
