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

type enquiryInput struct {
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

// createEnquiry stores an enquiry and queues Daw Mi's email. The visitor gets no email; the site shows the receipt.
func (h *Handler) createEnquiry(c *gin.Context) {
	var key string
	if header := c.GetHeader("Idempotency-Key"); header != "" {
		id, ok := web.ParseUUID(c, "Idempotency-Key", header)
		if !ok {
			return
		}
		key = id.String()
	}
	var in enquiryInput
	if !web.BindJSON(c, &in) {
		return
	}
	// The key's hash covers the body as decoded, so a retry that differs only in spacing is the same request.
	body, err := json.Marshal(in)
	if err != nil {
		web.Error(c, err)
		return
	}
	ctx := c.Request.Context()
	res, err := CreateEnquiry(ctx, h.Pool, Enquiry{
		IdempotencyKey:      key,
		Body:                body,
		Name:                in.Name,
		Email:               in.Email,
		Organisation:        in.Organisation,
		Subject:             in.Subject,
		EnquiryType:         in.EnquiryType,
		Service:             in.Service,
		Message:             in.Message,
		Locale:              in.Locale,
		PrivacyAcknowledged: in.PrivacyAcknowledged,
	}, h.Now())
	if err != nil {
		web.Error(c, err)
		return
	}
	if h.Queue != nil {
		h.Queue.Enqueue(ctx, res.Tasks...)
	}
	slog.InfoContext(ctx, "contact enquiry received", "request_id", web.RequestID(ctx),
		"enquiry_id", res.ID.String(), "replayed", res.Replayed)
	if res.Replayed {
		c.Header("Idempotent-Replayed", "true")
	}
	c.JSON(http.StatusCreated, res.Receipt)
}

type enquiryListQuery struct {
	Status string `form:"status" binding:"omitempty,oneof=new handled"`
	Search string `form:"q" binding:"max=100"`
	Cursor string `form:"cursor" binding:"max=200"`
	Limit  *int   `form:"limit" binding:"omitnil,min=1,max=100"`
}

type enquiryListResponse struct {
	Items      []enquiryResponse `json:"items"`
	NextCursor string            `json:"nextCursor,omitempty"`
}

func (h *Handler) listEnquiries(c *gin.Context) {
	var q enquiryListQuery
	if !web.BindQuery(c, &q) {
		return
	}
	f := EnquiryFilter{Status: q.Status, Search: q.Search, Cursor: q.Cursor}
	if q.Limit != nil {
		f.Limit = *q.Limit
	}
	page, err := ListEnquiries(c.Request.Context(), db.New(h.Pool), f)
	if err != nil {
		web.Error(c, err)
		return
	}
	out := enquiryListResponse{Items: make([]enquiryResponse, len(page.Items)), NextCursor: page.NextCursor}
	for i, row := range page.Items {
		out.Items[i] = enquiryView(db.GetContactEnquiryRow(row))
	}
	c.JSON(http.StatusOK, out)
}

func (h *Handler) getEnquiry(c *gin.Context) {
	id, ok := web.ParseUUID(c, "enquiryId", c.Param("enquiryId"))
	if !ok {
		return
	}
	e, err := GetEnquiry(c.Request.Context(), db.New(h.Pool), id)
	if err != nil {
		web.Error(c, err)
		return
	}
	c.JSON(http.StatusOK, enquiryView(e))
}

// markEnquiryHandled changes nothing the second time.
func (h *Handler) markEnquiryHandled(c *gin.Context) {
	id, ok := web.ParseUUID(c, "enquiryId", c.Param("enquiryId"))
	if !ok {
		return
	}
	session, _ := auth.FromContext(c.Request.Context())
	e, err := MarkHandled(c.Request.Context(), db.New(h.Pool), id, session.User.ID, h.Now())
	if err != nil {
		web.Error(c, err)
		return
	}
	c.JSON(http.StatusOK, enquiryView(e))
}

type enquiryResponse struct {
	ID           string          `json:"id"`
	Reference    string          `json:"reference"`
	Name         string          `json:"name"`
	Email        string          `json:"email"`
	Organisation string          `json:"organisation,omitempty"`
	EnquiryType  string          `json:"enquiryType"`
	Service      *serviceRefJSON `json:"service,omitempty"`
	Subject      string          `json:"subject,omitempty"`
	Message      string          `json:"message"`
	Locale       string          `json:"locale"`
	Status       string          `json:"status"`
	HandledAt    *time.Time      `json:"handledAt,omitempty"`
	HandledBy    string          `json:"handledBy,omitempty"`
	CreatedAt    time.Time       `json:"createdAt"`
}

type serviceRefJSON struct {
	ID   string          `json:"id"`
	Slug string          `json:"slug"`
	Name json.RawMessage `json:"name"`
}

func enquiryView(e db.GetContactEnquiryRow) enquiryResponse {
	v := enquiryResponse{
		ID:           e.ID.String(),
		Reference:    e.Reference,
		Name:         e.Name,
		Email:        e.Email,
		Organisation: e.Organisation.String,
		EnquiryType:  e.EnquiryType,
		Subject:      e.Subject.String,
		Message:      e.Message,
		Locale:       e.Locale,
		Status:       e.Status,
		HandledBy:    e.HandledBy.String(),
		CreatedAt:    e.CreatedAt.UTC(),
	}
	if e.ServiceID.Valid {
		v.Service = &serviceRefJSON{ID: e.ServiceID.String(), Slug: e.ServiceSlug.String, Name: e.ServiceName}
	}
	if e.HandledAt.Valid {
		v.HandledAt = new(e.HandledAt.Time.UTC())
	}
	return v
}
