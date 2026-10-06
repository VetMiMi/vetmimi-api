package comms

import "strings"

// Kind is what a message is about (docs/data-model.md, "communications"). The
// list mirrors CommunicationKind in openapi.yaml.
type Kind string

const (
	RequestReceived                 Kind = "request_received"
	BookingConfirmed                Kind = "booking_confirmed"
	RequestDeclined                 Kind = "request_declined"
	Rescheduled                     Kind = "rescheduled"
	Cancelled                       Kind = "cancelled"
	Reminder                        Kind = "reminder"
	RequestExpired                  Kind = "request_expired"
	PractitionerNewRequest          Kind = "practitioner_new_request"
	PractitionerNewBooking          Kind = "practitioner_new_booking"
	PractitionerClientCancelled     Kind = "practitioner_client_cancelled"
	PractitionerRescheduleRequested Kind = "practitioner_reschedule_requested"
	PractitionerNewEnquiry          Kind = "practitioner_new_enquiry"
)

// Kinds is every kind; each has a template per locale.
var Kinds = []Kind{
	RequestReceived, BookingConfirmed, RequestDeclined, Rescheduled, Cancelled, Reminder,
	RequestExpired, PractitionerNewRequest, PractitionerNewBooking, PractitionerClientCancelled,
	PractitionerRescheduleRequested, PractitionerNewEnquiry,
}

// Locales are the template locales, as appointments.locale allows.
var Locales = []string{"en", "my"}

// Audience is who a message goes to.
type Audience string

const (
	Visitor      Audience = "visitor"
	Practitioner Audience = "practitioner"
)

// Audience is Practitioner for the practitioner_ kinds, which go to Daw Mi.
func (k Kind) Audience() Audience {
	if strings.HasPrefix(string(k), "practitioner_") {
		return Practitioner
	}
	return Visitor
}
