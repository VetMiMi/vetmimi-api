package web

import (
	"encoding/json"
	"errors"
	"fmt"
	"maps"
	"net/http"
	"reflect"
	"regexp"
	"slices"
	"strconv"
	"strings"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/gin-gonic/gin/binding"
	"github.com/go-playground/validator/v10"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgtype"

	"github.com/VetMiMi/vetmimi-api/internal/apperr"
)

func init() {
	// A misspelt field is refused instead of silently ignored.
	binding.EnableDecoderDisallowUnknownFields = true

	v := binding.Validator.Engine().(*validator.Validate)
	v.RegisterTagNameFunc(func(f reflect.StructField) string {
		name, _, _ := strings.Cut(f.Tag.Get("json"), ",")
		if name == "" {
			name = f.Tag.Get("form")
		}
		return name
	})
	pattern := func(re *regexp.Regexp) validator.Func {
		return func(fl validator.FieldLevel) bool { return re.MatchString(fl.Field().String()) }
	}
	_ = v.RegisterValidation("slug", pattern(regexp.MustCompile(`^[a-z0-9]+(-[a-z0-9]+)*$`)))
	_ = v.RegisterValidation("phone", pattern(regexp.MustCompile(`^[0-9+() -]{6,32}$`)))
	_ = v.RegisterValidation("hhmm", pattern(regexp.MustCompile(`^([01][0-9]|2[0-3]):[0-5][0-9]$`)))
	_ = v.RegisterValidation("totp", pattern(regexp.MustCompile(`^[0-9]{6}$`)))
}

// BindJSON decodes and checks the body into v. When it returns false it has answered 400.
// The body is kept, so a handler may bind it again into another shape.
func BindJSON(c *gin.Context, v any) bool {
	err := c.ShouldBindBodyWithJSON(v)
	if err == nil {
		return true
	}
	if strings.HasPrefix(err.Error(), "json: unknown field ") {
		body, _ := c.Get(gin.BodyBytesKey)
		b, _ := body.([]byte)
		err = apperr.Invalid("The request is not valid.",
			apperr.FieldError{Field: unknownField(b, reflect.TypeOf(v)), Message: "is not an allowed property"})
	} else {
		err = invalid(err, "/")
	}
	Error(c, err)
	return false
}

// BindQuery decodes and checks the query string into v. When it returns false it has answered 400.
func BindQuery(c *gin.Context, v any) bool {
	if err := c.ShouldBindQuery(v); err != nil {
		Error(c, invalid(err, ""))
		return false
	}
	return true
}

// PathUUID reads the path parameter name as a UUID. When it returns false it has answered 400.
func PathUUID(c *gin.Context, name string) (pgtype.UUID, bool) {
	return ParseUUID(c, name, c.Param(name))
}

// ParseUUID reads value, from the request's field, as a UUID. When it returns false it has answered 400.
func ParseUUID(c *gin.Context, field, value string) (pgtype.UUID, bool) {
	id, err := uuid.Parse(value)
	if err != nil {
		Error(c, apperr.Invalid("The request is not valid.", apperr.FieldError{Field: field, Message: "must be a valid uuid"}))
		return pgtype.UUID{}, false
	}
	return pgtype.UUID{Bytes: id, Valid: true}, true
}

// invalid names each field that failed: as a JSON pointer in a body, by name in a query.
// Messages come from the rule, never the error text, which can quote the value.
func invalid(err error, prefix string) error {
	var tooBig *http.MaxBytesError
	if errors.As(err, &tooBig) {
		return apperr.New(apperr.PayloadTooLarge, fmt.Sprintf("The body is over its %d-byte limit.", tooBig.Limit))
	}
	var fields []apperr.FieldError
	var failed validator.ValidationErrors
	var wrongType *json.UnmarshalTypeError
	switch {
	case errors.As(err, &failed):
		for _, fe := range failed {
			fields = append(fields, apperr.FieldError{Field: fieldPath(fe.Namespace(), prefix), Message: rule(fe)})
		}
	case errors.As(err, &wrongType):
		fields = append(fields, apperr.FieldError{Field: prefix + strings.ReplaceAll(wrongType.Field, ".", "/"),
			Message: "must be of type " + jsonType(wrongType.Type)})
	default:
		fields = append(fields, apperr.FieldError{Message: "is missing or malformed"})
	}
	return apperr.Invalid("The request is not valid.", fields...)
}

// fieldPath turns "input.periods[0].startTime" into "/periods/0/startTime", or "startTime" in a query.
func fieldPath(namespace, prefix string) string {
	_, path, _ := strings.Cut(namespace, ".")
	path = strings.NewReplacer("[", ".", "]", "").Replace(path)
	if prefix == "" {
		return path
	}
	return prefix + strings.ReplaceAll(path, ".", "/")
}

func rule(fe validator.FieldError) string {
	items := fe.Kind() == reflect.Slice
	switch fe.Tag() {
	case "required":
		return "is required"
	case "email":
		return "must be a valid email"
	case "uuid":
		return "must be a valid uuid"
	case "datetime":
		if fe.Param() == time.DateOnly {
			return "must be a valid date"
		}
		return "must be a valid date-time"
	case "oneof":
		return "must be one of: " + strings.ReplaceAll(fe.Param(), " ", ", ")
	case "slug":
		return "must be lowercase words joined by hyphens"
	case "phone":
		return "must be a phone number"
	case "hhmm":
		return "must be a time as HH:MM"
	case "totp":
		return "must be six digits"
	case "min":
		if items {
			return "must have at least " + fe.Param() + " items"
		}
		if fe.Kind() == reflect.String {
			return "must be at least " + fe.Param() + " characters"
		}
		return "must be at least " + fe.Param()
	case "max":
		if items {
			return "must have at most " + fe.Param() + " items"
		}
		if fe.Kind() == reflect.String {
			return "must be at most " + fe.Param() + " characters"
		}
		return "must be at most " + fe.Param()
	case "unique":
		return "must not repeat an item"
	}
	return "is not valid"
}

func jsonType(t reflect.Type) string {
	switch t.Kind() {
	case reflect.Int, reflect.Int8, reflect.Int16, reflect.Int32, reflect.Int64,
		reflect.Uint, reflect.Uint8, reflect.Uint16, reflect.Uint32, reflect.Uint64:
		return "integer"
	case reflect.String:
		return "string"
	case reflect.Bool:
		return "boolean"
	case reflect.Slice, reflect.Array:
		return "array"
	case reflect.Struct, reflect.Map:
		return "object"
	}
	return "number"
}

// unknownField is the JSON pointer of the first key in body that t has no field for:
// encoding/json names the key but not the object it sits in.
func unknownField(body []byte, t reflect.Type) string {
	var doc any
	_ = json.Unmarshal(body, &doc)
	return findUnknown(doc, t, "")
}

func findUnknown(doc any, t reflect.Type, path string) string {
	for t.Kind() == reflect.Pointer {
		t = t.Elem()
	}
	switch doc := doc.(type) {
	case map[string]any:
		if t.Kind() != reflect.Struct {
			return ""
		}
		for _, key := range slices.Sorted(maps.Keys(doc)) {
			f, ok := fieldNamed(t, key)
			if !ok {
				return path + "/" + key
			}
			if p := findUnknown(doc[key], f.Type, path+"/"+key); p != "" {
				return p
			}
		}
	case []any:
		if t.Kind() != reflect.Slice && t.Kind() != reflect.Array {
			return ""
		}
		for i, item := range doc {
			if p := findUnknown(item, t.Elem(), path+"/"+strconv.Itoa(i)); p != "" {
				return p
			}
		}
	}
	return ""
}

// fieldNamed matches key the way encoding/json does: by tag or field name, ignoring case.
func fieldNamed(t reflect.Type, key string) (reflect.StructField, bool) {
	for i := range t.NumField() {
		f := t.Field(i)
		name, _, _ := strings.Cut(f.Tag.Get("json"), ",")
		if f.Anonymous && name == "" {
			embedded := f.Type
			if embedded.Kind() == reflect.Pointer {
				embedded = embedded.Elem()
			}
			if embedded.Kind() == reflect.Struct {
				if inner, ok := fieldNamed(embedded, key); ok {
					return inner, true
				}
				continue
			}
		}
		if name == "" {
			name = f.Name
		}
		if name != "-" && f.IsExported() && strings.EqualFold(name, key) {
			return f, true
		}
	}
	return reflect.StructField{}, false
}
