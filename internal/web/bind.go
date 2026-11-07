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

	validate := binding.Validator.Engine().(*validator.Validate)
	validate.RegisterTagNameFunc(func(field reflect.StructField) string {
		name, _, _ := strings.Cut(field.Tag.Get("json"), ",")
		if name == "" {
			name, _, _ = strings.Cut(field.Tag.Get("form"), ",")
		}
		if name == "" {
			name = field.Tag.Get("uri")
		}
		return name
	})
	pattern := func(expression *regexp.Regexp) validator.Func {
		return func(field validator.FieldLevel) bool { return expression.MatchString(field.Field().String()) }
	}
	// RegisterValidation fails only for an empty tag or a nil function.
	_ = validate.RegisterValidation("slug", pattern(regexp.MustCompile(`^[a-z0-9]+(-[a-z0-9]+)*$`)))
	_ = validate.RegisterValidation("phone", pattern(regexp.MustCompile(`^[0-9+() -]{6,32}$`)))
	_ = validate.RegisterValidation("hhmm", pattern(regexp.MustCompile(`^([01][0-9]|2[0-3]):[0-5][0-9]$`)))
	_ = validate.RegisterValidation("totp", pattern(regexp.MustCompile(`^[0-9]{6}$`)))
}

// BindJSON decodes and checks the body into target. When it returns false it has answered 400.
// The body is kept, so a handler may bind it again into another shape.
func BindJSON(c *gin.Context, target any) bool {
	err := c.ShouldBindBodyWithJSON(target)
	if err == nil {
		return true
	}
	stored, _ := c.Get(gin.BodyBytesKey)
	body, _ := stored.([]byte)
	if strings.HasPrefix(err.Error(), "json: unknown field ") {
		field := apperr.FieldError{Field: unknownField(body, reflect.TypeOf(target)), Message: "is not an allowed property"}
		Error(c, apperr.Invalid("The request is not valid.", field))
		return false
	}
	find := func() (string, reflect.StructField) { return undecodable(body, reflect.TypeOf(target), "") }
	Error(c, invalid(err, "/", find))
	return false
}

// BindQuery decodes and checks the query string into target. When it returns false it has answered 400.
func BindQuery(c *gin.Context, target any) bool {
	err := c.ShouldBindQuery(target)
	if err != nil {
		find := func() (string, reflect.StructField) { return badQueryKey(c.Request.URL.Query(), target) }
		Error(c, invalid(err, "", find))
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

// BindURI checks the path parameters into target. When it returns false it has answered 400.
func BindURI(c *gin.Context, target any) bool {
	err := c.ShouldBindUri(target)
	if err != nil {
		Error(c, invalid(err, "", nil))
		return false
	}
	return true
}

// invalid names each field that failed: as a JSON pointer in a body, by name in a query.
// Messages come from the rule, never the error text, which can quote the value.
// A value that fails to decode, such as a bad date-time, errs without its key, so find, when given,
// looks for it and returns it already named for the body or query.
func invalid(err error, prefix string, find func() (string, reflect.StructField)) error {
	var tooBig *http.MaxBytesError
	if errors.As(err, &tooBig) {
		return apperr.New(apperr.PayloadTooLarge, fmt.Sprintf("The body is over its %d-byte limit.", tooBig.Limit))
	}
	var fields []apperr.FieldError
	var failed validator.ValidationErrors
	var wrongType *json.UnmarshalTypeError
	switch {
	case errors.As(err, &failed):
		for _, failure := range failed {
			fields = append(fields, apperr.FieldError{Field: fieldPath(failure.Namespace(), prefix), Message: rule(failure)})
		}
	case errors.As(err, &wrongType):
		field := prefix + strings.ReplaceAll(wrongType.Field, ".", "/")
		fields = append(fields, apperr.FieldError{Field: field, Message: "must be of type " + jsonType(wrongType.Type)})
	default:
		var key string
		var field reflect.StructField
		if find != nil {
			key, field = find()
		}
		if key != "" {
			fields = append(fields, apperr.FieldError{Field: key, Message: mustBe(field)})
		} else {
			fields = append(fields, apperr.FieldError{Message: "is missing or malformed"})
		}
	}
	return apperr.Invalid("The request is not valid.", fields...)
}

// fieldPath turns "input.periods[0].startTime" into "/periods/0/startTime", or "status[0]" in a query into "status".
func fieldPath(namespace, prefix string) string {
	_, path, _ := strings.Cut(namespace, ".")
	path = strings.NewReplacer("[", ".", "]", "").Replace(path)
	if prefix == "" {
		// A query parameter is never nested, so a repeated one is named without its index.
		name, _, _ := strings.Cut(path, ".")
		return name
	}
	return prefix + strings.ReplaceAll(path, ".", "/")
}

func rule(failure validator.FieldError) string {
	items := failure.Kind() == reflect.Slice
	switch failure.Tag() {
	case "required":
		return "is required"
	case "email":
		return "must be a valid email"
	case "uuid":
		return "must be a valid uuid"
	case "datetime":
		if failure.Param() == time.DateOnly {
			return "must be a valid date"
		}
		return "must be a valid date-time"
	case "oneof":
		return "must be one of: " + strings.ReplaceAll(failure.Param(), " ", ", ")
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
			return "must have at least " + failure.Param() + " items"
		}
		if failure.Kind() == reflect.String {
			return "must be at least " + failure.Param() + " characters"
		}
		return "must be at least " + failure.Param()
	case "max":
		if items {
			return "must have at most " + failure.Param() + " items"
		}
		if failure.Kind() == reflect.String {
			return "must be at most " + failure.Param() + " characters"
		}
		return "must be at most " + failure.Param()
	case "unique":
		return "must not repeat an item"
	}
	return "is not valid"
}

func mustBe(field reflect.StructField) string {
	valueType := field.Type
	for valueType.Kind() == reflect.Pointer || valueType.Kind() == reflect.Slice {
		valueType = valueType.Elem()
	}
	switch {
	case valueType != reflect.TypeFor[time.Time]():
		return "must be of type " + jsonType(valueType)
	case field.Tag.Get("time_format") == time.DateOnly:
		return "must be a valid date"
	}
	return "must be a valid date-time"
}

// badQueryKey is the first query key whose value target cannot hold, such as limit=abc.
func badQueryKey(query map[string][]string, target any) (string, reflect.StructField) {
	structType := reflect.TypeOf(target).Elem()
	for i := range structType.NumField() {
		field := structType.Field(i)
		name, _, _ := strings.Cut(field.Tag.Get("form"), ",")
		values, ok := query[name]
		if ok && binding.MapFormWithTag(reflect.New(structType).Interface(), map[string][]string{name: values}, "form") != nil {
			return name, field
		}
	}
	return "", reflect.StructField{}
}

// undecodable is the JSON pointer of the first value in doc that its field cannot hold, such as a bad date-time.
func undecodable(doc []byte, valueType reflect.Type, path string) (string, reflect.StructField) {
	for valueType.Kind() == reflect.Pointer {
		valueType = valueType.Elem()
	}
	if valueType.Kind() != reflect.Struct || valueType == reflect.TypeFor[time.Time]() {
		return "", reflect.StructField{}
	}
	var object map[string]json.RawMessage
	err := json.Unmarshal(doc, &object)
	if err != nil {
		return "", reflect.StructField{}
	}
	for _, key := range slices.Sorted(maps.Keys(object)) {
		field, ok := fieldNamed(valueType, key)
		if !ok {
			continue
		}
		err = json.Unmarshal(object[key], reflect.New(field.Type).Interface())
		if err == nil {
			continue
		}
		pointer, inner := undecodable(object[key], field.Type, path+"/"+key)
		if pointer != "" {
			return pointer, inner
		}
		return path + "/" + key, field
	}
	return "", reflect.StructField{}
}

func jsonType(valueType reflect.Type) string {
	switch valueType.Kind() {
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

// unknownField is the JSON pointer of the first key in body that valueType has no field for:
// encoding/json names the key but not the object it sits in.
func unknownField(body []byte, valueType reflect.Type) string {
	var doc any
	err := json.Unmarshal(body, &doc)
	if err != nil {
		return ""
	}
	return findUnknown(doc, valueType, "")
}

func findUnknown(doc any, valueType reflect.Type, path string) string {
	for valueType.Kind() == reflect.Pointer {
		valueType = valueType.Elem()
	}
	switch value := doc.(type) {
	case map[string]any:
		if valueType.Kind() != reflect.Struct {
			return ""
		}
		for _, key := range slices.Sorted(maps.Keys(value)) {
			field, ok := fieldNamed(valueType, key)
			if !ok {
				return path + "/" + key
			}
			pointer := findUnknown(value[key], field.Type, path+"/"+key)
			if pointer != "" {
				return pointer
			}
		}
	case []any:
		if valueType.Kind() != reflect.Slice && valueType.Kind() != reflect.Array {
			return ""
		}
		for i, item := range value {
			pointer := findUnknown(item, valueType.Elem(), path+"/"+strconv.Itoa(i))
			if pointer != "" {
				return pointer
			}
		}
	}
	return ""
}

// fieldNamed matches key the way encoding/json does: by tag or field name, ignoring case.
func fieldNamed(structType reflect.Type, key string) (reflect.StructField, bool) {
	for i := range structType.NumField() {
		field := structType.Field(i)
		name, _, _ := strings.Cut(field.Tag.Get("json"), ",")
		if field.Anonymous && name == "" {
			embedded := field.Type
			if embedded.Kind() == reflect.Pointer {
				embedded = embedded.Elem()
			}
			if embedded.Kind() == reflect.Struct {
				inner, ok := fieldNamed(embedded, key)
				if ok {
					return inner, true
				}
				continue
			}
		}
		if name == "" {
			name = field.Name
		}
		if name != "-" && field.IsExported() && strings.EqualFold(name, key) {
			return field, true
		}
	}
	return reflect.StructField{}, false
}
