package httpapi

import (
	"cmp"
	"context"
	"errors"
	"fmt"
	"maps"
	"net/http"
	"net/mail"
	"slices"
	"strings"
	"sync"
	"time"

	"github.com/getkin/kin-openapi/openapi3"
	"github.com/getkin/kin-openapi/openapi3filter"
	nethttpmiddleware "github.com/oapi-codegen/nethttp-middleware"

	"github.com/VetMiMi/vetmimi-api/internal/apperr"
)

// validateRequests checks each request against spec, which it takes over,
// and answers every problem together as one 400.
func validateRequests(spec *openapi3.T) func(http.Handler) http.Handler {
	registerFormats.Do(defineFormats)

	// kin-openapi's 3.1 engine reduces each failure to a sentence quoting the
	// value. openapi.yaml only uses keywords 3.0 reads alike, and 3.0 returns
	// structured SchemaErrors.
	spec.OpenAPI = "3.0.3"

	return nethttpmiddleware.OapiRequestValidatorWithOptions(spec, &nethttpmiddleware.Options{
		Options: openapi3filter.Options{
			MultiError:          true,
			AuthenticationFunc:  openapi3filter.NoopAuthenticationFunc, // checked earlier in the chain
			SkipSettingDefaults: true,
		},
		// openapi.yaml names localhost as its server.
		DoNotValidateServers: true,
		ErrorHandlerWithOpts: answerInvalid,
	})
}

var registerFormats sync.Once

// defineFormats registers the string formats openapi.yaml uses. The registry
// is process-wide, hence the sync.Once; dates are checked by parsing, which is
// stricter than kin-openapi's patterns.
func defineFormats() {
	openapi3.DefineStringFormatValidator("uuid",
		openapi3.NewRegexpFormatValidator(openapi3.FormatOfStringForUUIDOfRFC9562))
	openapi3.DefineStringFormatValidator("email", openapi3.NewCallbackValidator(func(s string) error {
		if a, err := mail.ParseAddress(s); err != nil || a.Address != s {
			return errors.New("not a bare email address")
		}
		return nil
	}))
	openapi3.DefineStringFormatValidator("date", parsesAs(time.DateOnly))
	openapi3.DefineStringFormatValidator("date-time", parsesAs(time.RFC3339))
}

func parsesAs(layout string) openapi3.StringFormatValidator {
	return openapi3.NewCallbackValidator(func(s string) error {
		_, err := time.Parse(layout, s)
		return err
	})
}

// answerInvalid builds every message from the rule that failed, never from
// the error text, which quotes the submitted value.
func answerInvalid(_ context.Context, err error, w http.ResponseWriter, _ *http.Request, _ nethttpmiddleware.ErrorHandlerOpts) {
	if e := bodyTooLarge(err); e != nil {
		writeProblem(w, e)
		return
	}
	writeProblem(w, apperr.Invalid("The request does not match the API contract.", fieldErrors(err)...))
}

// fieldErrors lists each problem in err once, sorted. It uses type
// assertions, because errors.As would look through a RequestError.
func fieldErrors(err error) []apperr.FieldError {
	var all []apperr.FieldError
	for _, leaf := range leaves(err) {
		req, ok := leaf.(*openapi3filter.RequestError)
		switch {
		case ok && req.Parameter != nil:
			all = append(all, parameterProblems(req)...)
		case ok && req.RequestBody != nil:
			all = append(all, bodyProblems(req)...)
		default:
			all = append(all, apperr.FieldError{Message: "does not match the API contract"})
		}
	}
	slices.SortFunc(all, func(a, b apperr.FieldError) int {
		return cmp.Or(cmp.Compare(a.Field, b.Field), cmp.Compare(a.Message, b.Message))
	})
	return slices.Compact(all)
}

func parameterProblems(req *openapi3filter.RequestError) []apperr.FieldError {
	p := req.Parameter
	var parse *openapi3filter.ParseError
	switch {
	case errors.Is(req.Err, openapi3filter.ErrInvalidRequired):
		return []apperr.FieldError{{Field: p.Name, Message: "is required"}}
	case errors.As(req.Err, &parse) && p.Schema != nil:
		return []apperr.FieldError{{Field: p.Name, Message: mustBeType(p.Schema.Value)}}
	}
	var out []apperr.FieldError
	for _, leaf := range leaves(req.Err) {
		out = append(out, apperr.FieldError{Field: p.Name, Message: ruleMessage(leaf)})
	}
	return out
}

func bodyProblems(req *openapi3filter.RequestError) []apperr.FieldError {
	var parse *openapi3filter.ParseError
	switch {
	case errors.Is(req.Err, openapi3filter.ErrInvalidRequired):
		return []apperr.FieldError{{Message: "is required"}}
	case req.Err == nil:
		// Only an unaccepted Content-Type has no cause.
		types := slices.Sorted(maps.Keys(req.RequestBody.Content))
		return []apperr.FieldError{{Field: "Content-Type", Message: "must be " + strings.Join(types, " or ")}}
	case errors.As(req.Err, &parse):
		return []apperr.FieldError{{Message: "must be a well-formed body for its Content-Type"}}
	}
	var out []apperr.FieldError
	for _, leaf := range leaves(req.Err) {
		out = append(out, schemaProblems(leaf)...)
	}
	return out
}

func leaves(err error) []error {
	multi, ok := err.(openapi3.MultiError)
	if !ok {
		return []error{err}
	}
	var out []error
	for _, inner := range multi {
		out = append(out, leaves(inner)...)
	}
	return out
}

// schemaProblems names the body field err is about. An unknown property is
// reported at its own pointer, not at the object holding it.
func schemaProblems(err error) []apperr.FieldError {
	s, ok := err.(*openapi3.SchemaError)
	if !ok {
		return []apperr.FieldError{{Message: ruleMessage(err)}}
	}
	obj, isObject := s.Value.(map[string]any)
	if s.SchemaField != "properties" || !isObject {
		return []apperr.FieldError{{Field: pointer(s.JSONPointer()), Message: ruleMessage(s)}}
	}
	var out []apperr.FieldError
	for k := range obj {
		if _, known := s.Schema.Properties[k]; !known {
			out = append(out, apperr.FieldError{Field: pointer(append(s.JSONPointer(), k)), Message: "is not an allowed property"})
		}
	}
	return out
}

var pointerEscape = strings.NewReplacer("~", "~0", "/", "~1")

// pointer builds an RFC 6901 JSON pointer.
func pointer(path []string) string {
	var b strings.Builder
	for _, seg := range path {
		b.WriteString("/")
		b.WriteString(pointerEscape.Replace(seg))
	}
	return b.String()
}

func ruleMessage(err error) string {
	s, ok := err.(*openapi3.SchemaError)
	if !ok {
		return "does not match its schema"
	}
	schema := s.Schema
	switch s.SchemaField {
	case "required":
		return "is required"
	case "type":
		return mustBeType(schema)
	case "enum":
		values := make([]string, len(schema.Enum))
		for i, v := range schema.Enum {
			values[i] = fmt.Sprint(v)
		}
		return "must be one of: " + strings.Join(values, ", ")
	case "format":
		return "must be a valid " + schema.Format
	case "pattern":
		return "must match the pattern " + schema.Pattern
	case "minLength":
		return fmt.Sprintf("must be at least %d characters", schema.MinLength)
	case "maxLength":
		return fmt.Sprintf("must be at most %d characters", *schema.MaxLength)
	case "minimum":
		return fmt.Sprintf("must be at least %v", *schema.Min)
	case "maximum":
		return fmt.Sprintf("must be at most %v", *schema.Max)
	case "minItems":
		return fmt.Sprintf("must have at least %d items", schema.MinItems)
	case "maxItems":
		return fmt.Sprintf("must have at most %d items", *schema.MaxItems)
	case "uniqueItems":
		return "must not repeat an item"
	}
	return "does not match its schema"
}

func mustBeType(schema *openapi3.Schema) string {
	types := schema.Type.Slice()
	if len(types) == 0 {
		return "does not match its schema"
	}
	return "must be of type " + strings.Join(types, " or ")
}
