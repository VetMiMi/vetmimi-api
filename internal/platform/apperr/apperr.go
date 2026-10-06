// Package apperr holds the typed errors domain packages return. The handler
// layer maps each one, once, to an RFC 9457 Problem; this package knows the
// status for each code but nothing else about HTTP, so domain code can return
// these errors without importing net/http.
package apperr

import "time"

// Code is the stable, machine-readable name of a problem. The site maps it to
// localised wording, so renaming a code is a breaking change. The list must
// match the "Error model" table in docs/architecture.md.
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
	Unavailable              Code = "unavailable"
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
	Unavailable:              {503, "Service unavailable"},
}

// lookup treats a code missing from the table as an internal error, so a
// typo in a Code("...") conversion can never produce status 0.
func (c Code) lookup() kind {
	if k, ok := kinds[c]; ok {
		return k
	}
	return kinds[InternalError]
}

// Status is the HTTP status the code is answered with.
func (c Code) Status() int { return c.lookup().status }

// Title is the short, fixed summary of the code.
func (c Code) Title() string { return c.lookup().title }

// FieldError names one part of a request and what is wrong with it. Field is
// a JSON pointer into the body or a parameter name.
type FieldError struct {
	Field   string
	Message string
}

// Error is a problem a client can act on. Detail is for an administrator or
// developer and may reach the response body, so it carries ids only, never
// names, emails or notes.
type Error struct {
	Code       Code
	Detail     string
	Fields     []FieldError
	Extensions map[string]any
	// RetryAfter is how long a rate_limited client should wait. It travels
	// as the Retry-After header, not in the body.
	RetryAfter time.Duration
}

func (e *Error) Error() string {
	if e.Detail == "" {
		return string(e.Code)
	}
	return string(e.Code) + ": " + e.Detail
}

// New returns an Error with the given code and detail.
func New(code Code, detail string) *Error {
	return &Error{Code: code, Detail: detail}
}

// Invalid returns an invalid_request Error listing each field at fault.
func Invalid(detail string, fields ...FieldError) *Error {
	return &Error{Code: InvalidRequest, Detail: detail, Fields: fields}
}

// RateLimit returns a rate_limited Error telling the client to wait.
func RateLimit(wait time.Duration) *Error {
	return &Error{Code: RateLimited, Detail: "Too many requests; try again later.", RetryAfter: wait}
}
