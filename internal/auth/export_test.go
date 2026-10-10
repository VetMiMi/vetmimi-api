package auth

import "testing"

const DummyPasswordHash = dummyPasswordHash

// RecordPasswordChecks returns every hash sign-in checks a password against
// until the test ends.
func RecordPasswordChecks(t testing.TB) *[]string {
	checked := &[]string{}
	verifyPassword = func(password, encoded string) (bool, error) {
		*checked = append(*checked, encoded)
		return VerifyPassword(password, encoded)
	}
	t.Cleanup(func() { verifyPassword = VerifyPassword })
	return checked
}

// AcceptEveryPassword lets a test reach the checks after the password
// without hashing.
func AcceptEveryPassword(t testing.TB) {
	verifyPassword = func(string, string) (bool, error) { return true, nil }
	t.Cleanup(func() { verifyPassword = VerifyPassword })
}

func (s *Sessions) DummySecret() ([]byte, error) { return s.codes.Open(s.dummySecret) }

func FailuresKey(prefix, email string) string {
	return (&Lockout{prefix: prefix}).key("failures", email)
}
