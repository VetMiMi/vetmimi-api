package comms

import (
	"encoding/json"
	"log/slog"
	"net/http"
	"time"

	"github.com/gin-gonic/gin"

	"github.com/VetMiMi/vetmimi-api/internal/auth"
	"github.com/VetMiMi/vetmimi-api/internal/db"
	"github.com/VetMiMi/vetmimi-api/internal/web"
)

type listEnquiriesQuery struct {
	Status string `form:"status" binding:"omitempty,oneof=new handled"`
	Search string `form:"q" binding:"max=100"`
	Cursor string `form:"cursor" binding:"max=200"`
	Limit  *int   `form:"limit" binding:"omitnil,min=1,max=100"`
}

type createEnquiryBody struct {
	Name                string `json:"name" binding:"required,max=120"`
	Email               string `json:"email" binding:"required,email,max=254"`
	Organisation        string `json:"organisation,omitempty" binding:"max=200"`
	EnquiryType         string `json:"enquiryType" binding:"required,oneof=collaboration workshop speaking art_of_wellness media organisation general"`
	Service             string `json:"service,omitempty" binding:"omitempty,slug"`
	Subject             string `json:"subject,omitempty" binding:"max=200"`
	Message             string `json:"message" binding:"required,max=5000"`
	Locale              string `json:"locale" binding:"required,oneof=en my"`
	PrivacyAcknowledged bool   `json:"privacyAcknowledged"`
}

type enquiryListResponse struct {
	Items      []enquiryResponse `json:"items"`
	NextCursor string            `json:"nextCursor,omitempty"`
}

type enquiryResponse struct {
	ID           string           `json:"id"`
	Reference    string           `json:"reference"`
	Name         string           `json:"name"`
	Email        string           `json:"email"`
	Organisation string           `json:"organisation,omitempty"`
	EnquiryType  string           `json:"enquiryType"`
	Service      *serviceResponse `json:"service,omitempty"`
	Subject      string           `json:"subject,omitempty"`
	Message      string           `json:"message"`
	Locale       string           `json:"locale"`
	Status       string           `json:"status"`
	HandledAt    *time.Time       `json:"handledAt,omitempty"`
	HandledBy    string           `json:"handledBy,omitempty"`
	CreatedAt    time.Time        `json:"createdAt"`
}

type serviceResponse struct {
	ID   string          `json:"id"`
	Slug string          `json:"slug"`
	Name json.RawMessage `json:"name"`
}

// createEnquiry stores an enquiry and queues Daw Mi's email. The visitor gets no email; the site shows the receipt.
func (h *Handler) createEnquiry(c *gin.Context) {
	var key string
	header := c.GetHeader("Idempotency-Key")
	if header != "" {
		id, ok := web.ParseUUID(c, "Idempotency-Key", header)
		if !ok {
			return
		}
		key = id.String()
	}
	var body createEnquiryBody
	if !web.BindJSON(c, &body) {
		return
	}
	ctx := c.Request.Context()

	// The key's hash covers the body as decoded, so a retry that differs only in spacing is the same request.
	raw, err := json.Marshal(body)
	if err != nil {
		web.Error(c, err)
		return
	}
	enquiry := Enquiry{
		IdempotencyKey:      key,
		Body:                raw,
		Name:                body.Name,
		Email:               body.Email,
		Organisation:        body.Organisation,
		Subject:             body.Subject,
		EnquiryType:         body.EnquiryType,
		Service:             body.Service,
		Message:             body.Message,
		Locale:              body.Locale,
		PrivacyAcknowledged: body.PrivacyAcknowledged,
	}
	created, err := CreateEnquiry(ctx, h.Pool, enquiry, h.Now())
	if err != nil {
		web.Error(c, err)
		return
	}

	h.Queue.Enqueue(ctx, created.Tasks...)
	slog.InfoContext(ctx, "contact enquiry received",
		"request_id", web.RequestID(ctx),
		"enquiry_id", created.ID.String(),
		"replayed", created.Replayed,
	)

	if created.Replayed {
		c.Header("Idempotent-Replayed", "true")
	}
	c.JSON(http.StatusCreated, created.Receipt)
}

func (h *Handler) listEnquiries(c *gin.Context) {
	var query listEnquiriesQuery
	if !web.BindQuery(c, &query) {
		return
	}
	ctx := c.Request.Context()

	filter := EnquiryFilter{
		Status: query.Status,
		Search: query.Search,
		Cursor: query.Cursor,
	}
	if query.Limit != nil {
		filter.Limit = *query.Limit
	}
	page, err := ListEnquiries(ctx, db.New(h.Pool), filter)
	if err != nil {
		web.Error(c, err)
		return
	}

	response := enquiryListResponse{
		Items:      make([]enquiryResponse, len(page.Items)),
		NextCursor: page.NextCursor,
	}
	for i, row := range page.Items {
		response.Items[i] = newEnquiryResponse(db.GetContactEnquiryRow(row))
	}
	c.JSON(http.StatusOK, response)
}

func (h *Handler) getEnquiry(c *gin.Context) {
	enquiryID, ok := web.PathUUID(c, "enquiryId")
	if !ok {
		return
	}
	ctx := c.Request.Context()

	enquiry, err := GetEnquiry(ctx, db.New(h.Pool), enquiryID)
	if err != nil {
		web.Error(c, err)
		return
	}

	c.JSON(http.StatusOK, newEnquiryResponse(enquiry))
}

// markEnquiryHandled changes nothing the second time.
func (h *Handler) markEnquiryHandled(c *gin.Context) {
	enquiryID, ok := web.PathUUID(c, "enquiryId")
	if !ok {
		return
	}
	ctx := c.Request.Context()

	session, _ := auth.FromContext(ctx)
	enquiry, err := MarkHandled(ctx, db.New(h.Pool), enquiryID, session.User.ID, h.Now())
	if err != nil {
		web.Error(c, err)
		return
	}

	c.JSON(http.StatusOK, newEnquiryResponse(enquiry))
}

func newEnquiryResponse(enquiry db.GetContactEnquiryRow) enquiryResponse {
	response := enquiryResponse{
		ID:           enquiry.ID.String(),
		Reference:    enquiry.Reference,
		Name:         enquiry.Name,
		Email:        enquiry.Email,
		Organisation: enquiry.Organisation.String,
		EnquiryType:  enquiry.EnquiryType,
		Subject:      enquiry.Subject.String,
		Message:      enquiry.Message,
		Locale:       enquiry.Locale,
		Status:       enquiry.Status,
		HandledBy:    enquiry.HandledBy.String(),
		CreatedAt:    enquiry.CreatedAt.UTC(),
	}
	if enquiry.ServiceID.Valid {
		response.Service = &serviceResponse{
			ID:   enquiry.ServiceID.String(),
			Slug: enquiry.ServiceSlug.String,
			Name: enquiry.ServiceName,
		}
	}
	if enquiry.HandledAt.Valid {
		response.HandledAt = new(enquiry.HandledAt.Time.UTC())
	}
	return response
}
