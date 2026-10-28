package web

import (
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"reflect"
	"regexp"
	"strings"

	"github.com/gin-gonic/gin"
	"github.com/gin-gonic/gin/binding"
	"github.com/go-playground/validator/v10"

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
func BindJSON(c *gin.Context, v any) bool {
	if err := c.ShouldBindJSON(v); err != nil {
		Error(c, invalid(err, "/"))
		return false
	}
	return true
}

// BindQuery decodes and checks the query string into v. When it returns false it has answered 400.
func BindQuery(c *gin.Context, v any) bool {
	if err := c.ShouldBindQuery(v); err != nil {
		Error(c, invalid(err, ""))
		return false
	}
	return true
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
	case strings.HasPrefix(err.Error(), "json: unknown field "):
		name := strings.Trim(strings.TrimPrefix(err.Error(), "json: unknown field "), `"`)
		fields = append(fields, apperr.FieldError{Field: prefix + name, Message: "is not an allowed property"})
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
