package comms

import "slices"

// Status is where a communication stands. It mirrors CommunicationStatus in
// openapi.yaml and is kept apart from the appointment's status: a failed
// email never changes a booking.
type Status string

const (
	StatusQueued    Status = "queued"
	StatusSent      Status = "sent"
	StatusFailed    Status = "failed"
	StatusCancelled Status = "cancelled"
)

// Statuses is every status.
var Statuses = []Status{StatusQueued, StatusSent, StatusFailed, StatusCancelled}

// transitions is every status change allowed. Sent, failed and cancelled are
// final: Resend and Mark as communicated insert new rows, so the history
// stays truthful.
var transitions = map[Status][]Status{
	StatusQueued: {StatusSent, StatusFailed, StatusCancelled},
}

// CanTransition reports whether a communication may move from one status to
// another. Every status change goes through it.
func CanTransition(from, to Status) bool {
	return slices.Contains(transitions[from], to)
}
