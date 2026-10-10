package httpapi

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"maps"
	"mime/multipart"
	"net/http"
	"time"
	"unicode/utf8"

	"github.com/jackc/pgx/v5/pgtype"
	openapi_types "github.com/oapi-codegen/runtime/types"

	"github.com/VetMiMi/vetmimi-api/internal/apperr"
	"github.com/VetMiMi/vetmimi-api/internal/db"
	"github.com/VetMiMi/vetmimi-api/internal/httpapi/gen"
	"github.com/VetMiMi/vetmimi-api/internal/media"
)

const (
	uploadMediaPattern = "/admin/media"
	uploadTimeout      = 120 * time.Second
	uploadBodyCap      = 21 << 20
	maxDescription     = 300 // the longest alt text or credit, as in openapi.yaml
)

var uploadMediaOperation = operation{ID: "uploadMedia", Security: schemeSessionToken}

func (s *server) ListMedia(ctx context.Context, req gen.ListMediaRequestObject) (gen.ListMediaResponseObject, error) {
	p := req.Params
	page, err := media.List(ctx, db.New(s.Pool), media.Filter{
		Search: deref(p.Q), Cursor: deref(p.Cursor), Limit: deref(p.Limit),
	})
	if err != nil {
		return nil, err
	}
	out := gen.ListMedia200JSONResponse{Items: make([]gen.Media, len(page.Items)), NextCursor: nonEmpty(page.NextCursor)}
	for i, m := range page.Items {
		out.Items[i] = s.mediaView(m)
	}
	return out, nil
}

func (s *server) GetMedia(ctx context.Context, req gen.GetMediaRequestObject) (gen.GetMediaResponseObject, error) {
	m, err := media.Get(ctx, db.New(s.Pool), uuid(req.MediaId))
	if err != nil {
		return nil, err
	}
	return gen.GetMedia200JSONResponse(s.mediaView(m)), nil
}

func (s *server) UpdateMedia(ctx context.Context, req gen.UpdateMediaRequestObject) (gen.UpdateMediaResponseObject, error) {
	b := req.Body
	var alt json.RawMessage
	if b.Alt != nil {
		alt, _ = json.Marshal(b.Alt) // two optional strings always marshal
	}
	m, err := media.Describe(ctx, db.New(s.Pool), uuid(req.MediaId), int32(b.Version),
		media.Description{Alt: alt, Credit: deref(b.Credit)}, s.Now())
	if err != nil {
		return nil, err
	}
	s.Log.InfoContext(ctx, "media_described", "request_id", RequestID(ctx), "media_id", m.ID.String())
	return gen.UpdateMedia200JSONResponse(s.mediaView(m)), nil
}

func (s *server) DeleteMedia(ctx context.Context, req gen.DeleteMediaRequestObject) (gen.DeleteMediaResponseObject, error) {
	if err := media.Delete(ctx, s.Pool, s.Media, uuid(req.MediaId)); err != nil {
		return nil, err
	}
	s.Log.InfoContext(ctx, "media_deleted", "request_id", RequestID(ctx), "media_id", req.MediaId.String())
	return gen.DeleteMedia204Response{}, nil
}

// uploadMedia is mounted by hand so it can stream its multipart body. It
// answers like the generated handlers.
func (s *server) uploadMedia(w http.ResponseWriter, r *http.Request) {
	data, d, err := readUpload(r)
	if err != nil {
		responseError(s.Log)(w, r, err)
		return
	}
	m, err := media.Upload(r.Context(), s.Pool, s.Media, data, d, actor(r.Context()), s.Now())
	if err != nil {
		responseError(s.Log)(w, r, err)
		return
	}
	s.Log.InfoContext(r.Context(), "media_uploaded", "request_id", RequestID(r.Context()),
		"media_id", m.ID.String(), "bytes", m.ByteSize)
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusCreated)
	_ = json.NewEncoder(w).Encode(s.mediaView(m))
}

var errNotMultipart = apperr.Invalid("Upload the image as multipart/form-data.",
	apperr.FieldError{Field: "Content-Type", Message: "must be multipart/form-data"})

// readUpload reads the file and description parts. The body cap stops it
// past 21 MiB; media.Upload holds the file to 20 MB.
func readUpload(r *http.Request) ([]byte, media.Description, error) {
	parts, err := r.MultipartReader()
	if err != nil {
		return nil, media.Description{}, errNotMultipart
	}
	u := upload{alt: map[string]string{}}
	for {
		part, err := parts.NextPart()
		if errors.Is(err, io.EOF) {
			break
		}
		if err == nil {
			err = u.read(part)
		}
		if err != nil {
			return nil, media.Description{}, uploadReadError(err)
		}
	}
	return u.file, u.description(), nil
}

type upload struct {
	file   []byte
	alt    map[string]string
	credit string
}

func (u *upload) read(part *multipart.Part) error {
	var err error
	switch part.FormName() {
	case "file":
		u.file, err = io.ReadAll(part)
	case "altEn":
		u.alt["en"], err = formValue(part)
	case "altMy":
		u.alt["my"], err = formValue(part)
	case "credit":
		u.credit, err = formValue(part)
	default:
		err = apperr.Invalid("The upload has an unknown part.",
			apperr.FieldError{Field: part.FormName(), Message: "is not an allowed property"})
	}
	return err
}

// description leaves out empty alt texts.
func (u *upload) description() media.Description {
	d := media.Description{Credit: u.credit}
	maps.DeleteFunc(u.alt, func(_, v string) bool { return v == "" })
	if len(u.alt) > 0 {
		d.Alt, _ = json.Marshal(u.alt)
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

// uploadReadError answers a body over the cap 413, as the strict server
// does, and a malformed one 400.
func uploadReadError(err error) error {
	if e := bodyTooLarge(err); e != nil {
		return e
	}
	var problem *apperr.Error
	if errors.As(err, &problem) {
		return problem
	}
	return apperr.Invalid("The upload is not well-formed multipart/form-data.")
}

func (s *server) mediaView(m db.Media) gen.Media {
	v := gen.Media{
		Id:         openapi_types.UUID(m.ID.Bytes),
		Width:      int(m.Width),
		Height:     int(m.Height),
		ByteSize:   int(m.ByteSize),
		Credit:     optionalString(m.Credit),
		Sizes:      s.imageSizes(m.ID, m.Widths),
		UploadedBy: uuidView(m.UploadedBy),
		Version:    int(m.Version),
		CreatedAt:  m.CreatedAt.UTC(),
		UpdatedAt:  m.UpdatedAt.UTC(),
	}
	if m.Alt != nil {
		var alt gen.MediaAlt
		if json.Unmarshal(m.Alt, &alt) == nil {
			v.Alt = &alt
		}
	}
	return v
}

func (s *server) imageSizes(id pgtype.UUID, widths []int32) []gen.ImageSize {
	sizes := media.Sizes(s.MediaPublicURL, id, widths)
	out := make([]gen.ImageSize, len(sizes))
	for i, size := range sizes {
		out[i] = gen.ImageSize{Width: int(size.Width), Url: size.URL}
	}
	return out
}
