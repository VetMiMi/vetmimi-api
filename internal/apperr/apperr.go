// Package apperr holds the typed errors domain packages return. httpapi maps
// each one to an RFC 9457 Problem, using the status its Code carries.
package apperr

import "time"

// Code is the stable name of a problem; the site maps it to localised wording,
// so renaming one is a breaking change. docs/architecture.md lists them all.
type Code string

const (
	InvalidRequest           Code = "invalid_request"
	Unauthenticated          Code = "unauthenticated"
	InvalidCredentials       Code = "invalid_credentials"
	Forbidden                Code = "forbidden"
	NotFound                 Code = "not_found"
	SlotUnavailable          Code = "slot_unavailable"
	StaleVersion             Code = "stale_version"
	InvalidTransition        Code = "invalid_transition"
	SlugTaken                Code = "slug_taken"
	OverlappingPeriod        Code = "overlapping_period"
	InUse                    Code = "in_use"
	PayloadTooLarge          Code = "payload_too_large"
	UnsupportedMediaType     Code = "unsupported_media_type"
	OutsideBookingWindow     Code = "outside_booking_window"
	ServiceNotBookable       Code = "service_not_bookable"
	BookingPaused            Code = "booking_paused"
	AcknowledgementRequired  Code = "acknowledgement_required"
	IdempotencyKeyReused     Code = "idempotency_key_reused"
	ActionNotAllowed         Code = "action_not_allowed"
	PublishRequirementsUnmet Code = "publish_requirements_unmet"
	RateLimited              Code = "rate_limited"
	InternalError            Code = "internal_error"
	AIFailed                 Code = "ai_failed"
	Unavailable              Code = "unavailable"
	FeatureUnavailable       Code = "feature_unavailable"
)

type kind struct {
	status int
	title  string
}

var kinds = map[Code]kind{
	InvalidRequest:           {400, "Invalid request"},
	Unauthenticated:          {401, "Not signed in"},
	InvalidCredentials:       {401, "Invalid credentials"},
	Forbidden:                {403, "Forbidden"},
	NotFound:                 {404, "Not found"},
	SlotUnavailable:          {409, "Time no longer available"},
	StaleVersion:             {409, "Changed by someone else"},
	InvalidTransition:        {409, "Status change not allowed"},
	SlugTaken:                {409, "Slug already in use"},
	OverlappingPeriod:        {409, "Overlapping availability period"},
	InUse:                    {409, "Still in use"},
	PayloadTooLarge:          {413, "Payload too large"},
	UnsupportedMediaType:     {415, "Unsupported media type"},
	OutsideBookingWindow:     {422, "Outside the booking window"},
	ServiceNotBookable:       {422, "Service not bookable"},
	BookingPaused:            {422, "Booking paused"},
	AcknowledgementRequired:  {422, "Acknowledgement required"},
	IdempotencyKeyReused:     {422, "Idempotency key reused"},
	ActionNotAllowed:         {422, "Action not allowed"},
	PublishRequirementsUnmet: {422, "Publishing requirements not met"},
	RateLimited:              {429, "Too many requests"},
	InternalError:            {500, "Internal error"},
	AIFailed:                 {502, "AI assistant failed"},
	Unavailable:              {503, "Service unavailable"},
	FeatureUnavailable:       {503, "Feature unavailable"},
}

// lookup treats an unknown code as an internal error, never status 0.
func (c Code) lookup() kind {
	if k, ok := kinds[c]; ok {
		return k
	}
	return kinds[InternalError]
}

func (c Code) Status() int { return c.lookup().status }

func (c Code) Title() string { return c.lookup().title }

// FieldError names one part of a request, as a JSON pointer into the body or
// a parameter name, and what is wrong with it.
type FieldError struct {
	Field   string
	Message string
}

// Error is a problem a client can act on. Detail may reach the response body,
// so it carries ids only, never names, emails or notes.
type Error struct {
	Code       Code
	Detail     string
	Fields     []FieldError
	Extensions map[string]any
	// RetryAfter is sent as the Retry-After header of a rate_limited answer.
	RetryAfter time.Duration
}

func (e *Error) Error() string {
	if e.Detail == "" {
		return string(e.Code)
	}
	return string(e.Code) + ": " + e.Detail
}

func New(code Code, detail string) *Error {
	return &Error{Code: code, Detail: detail}
}

func Invalid(detail string, fields ...FieldError) *Error {
	return &Error{Code: InvalidRequest, Detail: detail, Fields: fields}
}

func RateLimit(wait time.Duration) *Error {
	return &Error{Code: RateLimited, Detail: "Too many requests; try again later.", RetryAfter: wait}
}
