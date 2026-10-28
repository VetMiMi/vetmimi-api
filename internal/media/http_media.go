package media

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"maps"
	"mime/multipart"
	"net/http"
	"time"
	"unicode/utf8"

	"github.com/gin-gonic/gin"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgtype"

	"github.com/VetMiMi/vetmimi-api/internal/apperr"
	"github.com/VetMiMi/vetmimi-api/internal/auth"
	"github.com/VetMiMi/vetmimi-api/internal/db"
	"github.com/VetMiMi/vetmimi-api/internal/web"
)

const maxDescription = 300 // the longest alt text or credit

type altText struct {
	En *string `json:"en,omitempty" binding:"omitnil,max=300"`
	My *string `json:"my,omitempty" binding:"omitnil,max=300"`
}

type mediaJSON struct {
	ID         string    `json:"id"`
	Width      int32     `json:"width"`
	Height     int32     `json:"height"`
	ByteSize   int64     `json:"byteSize"`
	Alt        *altText  `json:"alt,omitempty"`
	Credit     string    `json:"credit,omitempty"`
	Sizes      []Size    `json:"sizes"`
	UploadedBy string    `json:"uploadedBy,omitempty"`
	Version    int32     `json:"version"`
	CreatedAt  time.Time `json:"createdAt"`
	UpdatedAt  time.Time `json:"updatedAt"`
}

type mediaListJSON struct {
	Items      []mediaJSON `json:"items"`
	NextCursor string      `json:"nextCursor,omitempty"`
}

type listQuery struct {
	Q      string `form:"q" binding:"max=100"`
	Cursor string `form:"cursor" binding:"max=200"`
	Limit  int    `form:"limit,default=50" binding:"min=1,max=100"`
}

func (h *Handler) listMedia(c *gin.Context) {
	var q listQuery
	if !web.BindQuery(c, &q) {
		return
	}
	page, err := List(c.Request.Context(), db.New(h.Pool), Filter{Search: q.Q, Cursor: q.Cursor, Limit: q.Limit})
	if err != nil {
		web.Error(c, err)
		return
	}
	out := mediaListJSON{Items: make([]mediaJSON, len(page.Items)), NextCursor: page.NextCursor}
	for i, m := range page.Items {
		out.Items[i] = h.view(m)
	}
	c.JSON(http.StatusOK, out)
}

func (h *Handler) getMedia(c *gin.Context) {
	id, ok := mediaID(c)
	if !ok {
		return
	}
	m, err := Get(c.Request.Context(), db.New(h.Pool), id)
	if err != nil {
		web.Error(c, err)
		return
	}
	c.JSON(http.StatusOK, h.view(m))
}

type describeInput struct {
	Version int      `json:"version" binding:"required,min=1"`
	Alt     *altText `json:"alt"`
	Credit  string   `json:"credit" binding:"max=300"`
}

// updateMedia replaces the alt text and credit; one left out is cleared.
func (h *Handler) updateMedia(c *gin.Context) {
	id, ok := mediaID(c)
	if !ok {
		return
	}
	var in describeInput
	if !web.BindJSON(c, &in) {
		return
	}
	d := Description{Credit: in.Credit}
	if in.Alt != nil {
		d.Alt, _ = json.Marshal(in.Alt) // two optional strings always marshal
	}
	ctx := c.Request.Context()
	m, err := Describe(ctx, db.New(h.Pool), id, int32(in.Version), d, h.Now())
	if err != nil {
		web.Error(c, err)
		return
	}
	slog.InfoContext(ctx, "media_described", "request_id", web.RequestID(ctx), "media_id", m.ID.String())
	c.JSON(http.StatusOK, h.view(m))
}

func (h *Handler) deleteMedia(c *gin.Context) {
	id, ok := mediaID(c)
	if !ok {
		return
	}
	ctx := c.Request.Context()
	if err := Delete(ctx, h.Pool, h.Store, id); err != nil {
		web.Error(c, err)
		return
	}
	slog.InfoContext(ctx, "media_deleted", "request_id", web.RequestID(ctx), "media_id", id.String())
	c.Status(http.StatusNoContent)
}

// uploadMedia streams its multipart body instead of binding it.
func (h *Handler) uploadMedia(c *gin.Context) {
	data, d, err := readUpload(c.Request)
	if err != nil {
		web.Error(c, err)
		return
	}
	ctx := c.Request.Context()
	session, _ := auth.FromContext(ctx)
	m, err := Upload(ctx, h.Pool, h.Store, data, d, session.User.ID, h.Now())
	if err != nil {
		web.Error(c, err)
		return
	}
	slog.InfoContext(ctx, "media_uploaded", "request_id", web.RequestID(ctx),
		"media_id", m.ID.String(), "bytes", m.ByteSize)
	c.JSON(http.StatusCreated, h.view(m))
}

// mediaID reads the path's id. When it returns false it has answered 400.
func mediaID(c *gin.Context) (pgtype.UUID, bool) {
	id, err := uuid.Parse(c.Param("mediaId"))
	if err != nil {
		web.Error(c, apperr.Invalid("The request is not valid.",
			apperr.FieldError{Field: "mediaId", Message: "must be a valid uuid"}))
		return pgtype.UUID{}, false
	}
	return pgtype.UUID{Bytes: id, Valid: true}, true
}

var errNotMultipart = apperr.Invalid("Upload the image as multipart/form-data.",
	apperr.FieldError{Field: "Content-Type", Message: "must be multipart/form-data"})

// readUpload reads the file and description parts. The body cap stops it
// past 21 MiB; Upload holds the file to 20 MB.
func readUpload(r *http.Request) ([]byte, Description, error) {
	parts, err := r.MultipartReader()
	if err != nil {
		return nil, Description{}, errNotMultipart
	}
	f := uploadForm{alt: map[string]string{}}
	for {
		part, err := parts.NextPart()
		if errors.Is(err, io.EOF) {
			break
		}
		if err == nil {
			err = f.read(part)
		}
		if err != nil {
			return nil, Description{}, uploadReadError(err)
		}
	}
	return f.file, f.description(), nil
}

type uploadForm struct {
	file   []byte
	alt    map[string]string
	credit string
}

func (f *uploadForm) read(part *multipart.Part) error {
	var err error
	switch part.FormName() {
	case "file":
		f.file, err = io.ReadAll(part)
	case "altEn":
		f.alt["en"], err = formValue(part)
	case "altMy":
		f.alt["my"], err = formValue(part)
	case "credit":
		f.credit, err = formValue(part)
	default:
		err = apperr.Invalid("The upload has an unknown part.",
			apperr.FieldError{Field: part.FormName(), Message: "is not an allowed property"})
	}
	return err
}

// description leaves out empty alt texts.
func (f *uploadForm) description() Description {
	d := Description{Credit: f.credit}
	maps.DeleteFunc(f.alt, func(_, v string) bool { return v == "" })
	if len(f.alt) > 0 {
		d.Alt, _ = json.Marshal(f.alt)
	}
	return d
}

func formValue(part *multipart.Part) (string, error) {
	b, err := io.ReadAll(io.LimitReader(part, 4*maxDescription+1))
	if err != nil {
		return "", err
	}
	if !utf8.Valid(b) || utf8.RuneCount(b) > maxDescription {
		return "", apperr.Invalid("A description is too long.", apperr.FieldError{
			Field: part.FormName(), Message: fmt.Sprintf("must be at most %d characters", maxDescription)})
	}
	return string(b), nil
}

// uploadReadError answers a body over the cap 413 and a malformed one 400.
func uploadReadError(err error) error {
	var tooBig *http.MaxBytesError
	if errors.As(err, &tooBig) {
		return apperr.New(apperr.PayloadTooLarge, fmt.Sprintf("The body is over its %d-byte limit.", tooBig.Limit))
	}
	var problem *apperr.Error
	if errors.As(err, &problem) {
		return problem
	}
	return apperr.Invalid("The upload is not well-formed multipart/form-data.")
}

func (h *Handler) view(m db.Media) mediaJSON {
	v := mediaJSON{
		ID:        m.ID.String(),
		Width:     m.Width,
		Height:    m.Height,
		ByteSize:  m.ByteSize,
		Credit:    m.Credit.String,
		Sizes:     Sizes(h.PublicURL, m.ID, m.Widths),
		Version:   m.Version,
		CreatedAt: m.CreatedAt.UTC(),
		UpdatedAt: m.UpdatedAt.UTC(),
	}
	if m.UploadedBy.Valid {
		v.UploadedBy = m.UploadedBy.String()
	}
	if m.Alt != nil {
		var alt altText
		if json.Unmarshal(m.Alt, &alt) == nil {
			v.Alt = &alt
		}
	}
	return v
}
