package booking

import "slices"

// Status is where an appointment stands (docs/data-model.md, "Appointments").
type Status string

const (
	Pending                 Status = "pending"
	Confirmed               Status = "confirmed"
	Declined                Status = "declined"
	Expired                 Status = "expired"
	CancelledByClient       Status = "cancelled_by_client"
	CancelledByPractitioner Status = "cancelled_by_practitioner"
	Completed               Status = "completed"
	NoShow                  Status = "no_show"
)

// transitions is every status change allowed. Pending is a request holding its
// slot, never a booking, so it reaches completed or no_show only through
// confirmed. A status with no entry is final.
var transitions = map[Status][]Status{
	Pending:   {Confirmed, Declined, Expired, CancelledByClient},
	Confirmed: {CancelledByClient, CancelledByPractitioner, Completed, NoShow},
}

// CanTransition reports whether an appointment may move from one status to
// another. Every status change goes through it.
func CanTransition(from, to Status) bool {
	return slices.Contains(transitions[from], to)
}

// Terminal reports whether s is final.
func Terminal(s Status) bool {
	return len(transitions[s]) == 0
}
