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
	"unicode/utf8"

	"github.com/gin-gonic/gin"

	"github.com/VetMiMi/vetmimi-api/internal/apperr"
	"github.com/VetMiMi/vetmimi-api/internal/auth"
	"github.com/VetMiMi/vetmimi-api/internal/web"
)

const maxDescription = 300 // the longest alt text or credit

var errNotMultipart = apperr.Invalid("Upload the image as multipart/form-data.",
	apperr.FieldError{Field: "Content-Type", Message: "must be multipart/form-data"})

// uploadForm is the multipart body, read part by part.
type uploadForm struct {
	file   []byte
	alt    map[string]string
	credit string
}

// uploadMedia streams its multipart body instead of binding it.
func (h *Handler) uploadMedia(c *gin.Context) {
	data, description, err := readUpload(c.Request)
	if err != nil {
		web.Error(c, err)
		return
	}
	ctx := c.Request.Context()

	// Safe on admin routes: web.SignedIn has already found the session.
	session, _ := auth.FromContext(ctx)
	item, err := Upload(ctx, h.Pool, h.Store, data, description, session.User.ID, h.Now())
	if err != nil {
		web.Error(c, err)
		return
	}

	slog.InfoContext(ctx, "media_uploaded",
		"request_id", web.RequestID(ctx),
		"media_id", item.ID.String(),
		"bytes", item.ByteSize,
	)
	c.JSON(http.StatusCreated, h.newMediaResponse(item))
}

// readUpload reads the file and description parts. The body cap stops it
// past 21 MiB; Upload holds the file to 20 MB.
func readUpload(request *http.Request) ([]byte, Description, error) {
	parts, err := request.MultipartReader()
	if err != nil {
		return nil, Description{}, errNotMultipart
	}
	form := uploadForm{alt: map[string]string{}}
	for {
		part, err := parts.NextPart()
		if errors.Is(err, io.EOF) {
			break
		}
		if err == nil {
			err = form.read(part)
		}
		if err != nil {
			return nil, Description{}, uploadReadError(err)
		}
	}
	return form.file, form.description(), nil
}

func (form *uploadForm) read(part *multipart.Part) error {
	var err error
	switch part.FormName() {
	case "file":
		form.file, err = io.ReadAll(part)
	case "altEn":
		form.alt["en"], err = formValue(part)
	case "altMy":
		form.alt["my"], err = formValue(part)
	case "credit":
		form.credit, err = formValue(part)
	default:
		err = apperr.Invalid("The upload has an unknown part.",
			apperr.FieldError{Field: part.FormName(), Message: "is not an allowed property"})
	}
	return err
}

// description leaves out empty alt texts.
func (form *uploadForm) description() Description {
	description := Description{Credit: form.credit}
	maps.DeleteFunc(form.alt, func(_, value string) bool { return value == "" })
	if len(form.alt) > 0 {
		description.Alt, _ = json.Marshal(form.alt) // a map of strings always marshals
	}
	return description
}

func formValue(part *multipart.Part) (string, error) {
	value, err := io.ReadAll(io.LimitReader(part, 4*maxDescription+1))
	if err != nil {
		return "", err
	}
	if !utf8.Valid(value) || utf8.RuneCount(value) > maxDescription {
		return "", apperr.Invalid("A description is too long.", apperr.FieldError{
			Field:   part.FormName(),
			Message: fmt.Sprintf("must be at most %d characters", maxDescription),
		})
	}
	return string(value), nil
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
