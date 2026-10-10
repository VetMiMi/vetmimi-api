package tokens

import "crypto/rand"

// crockford is Crockford's base32 alphabet: no I, L, O or U, so a reference
// read aloud or typed from an email is hard to get wrong.
const crockford = "0123456789ABCDEFGHJKMNPQRSTVWXYZ"

// NewReference returns the code people see for an appointment (VM-) or an
// enquiry (EN-): prefix and six random characters. A collision is caught by
// the table's unique index and retried.
func NewReference(prefix string) (string, error) {
	b := make([]byte, 6)
	if _, err := rand.Read(b); err != nil {
		return "", err
	}
	ref := []byte(prefix)
	for _, c := range b {
		ref = append(ref, crockford[c&31])
	}
	return string(ref), nil
}
