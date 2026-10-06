package booking

import "crypto/rand"

// crockford is Crockford's base32 alphabet: no I, L, O or U, so a reference
// read aloud or typed from an email is hard to get wrong.
const crockford = "0123456789ABCDEFGHJKMNPQRSTVWXYZ"

// NewReference returns the code people see for an appointment: VM- and six
// random Crockford base32 characters. About a billion values; a collision is
// caught by the unique index and retried (InsertAppointment).
func NewReference() (string, error) {
	b := make([]byte, 6)
	if _, err := rand.Read(b); err != nil {
		return "", err
	}
	ref := []byte("VM-")
	for _, c := range b {
		ref = append(ref, crockford[c&31])
	}
	return string(ref), nil
}
