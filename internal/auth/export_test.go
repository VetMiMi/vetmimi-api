package auth

import "testing"

// DummyPasswordHash is the hash an unknown email's password is checked
// against.
const DummyPasswordHash = dummyPasswordHash

// RecordPasswordChecks makes sign-in note every hash it checks a password
// against, until the test ends.
func RecordPasswordChecks(t testing.TB) *[]string {
	checked := &[]string{}
	verifyPassword = func(password, encoded string) (bool, error) {
		*checked = append(*checked, encoded)
		return VerifyPassword(password, encoded)
	}
	t.Cleanup(func() { verifyPassword = VerifyPassword })
	return checked
}

// AcceptEveryPassword makes sign-in treat every password as right, until the
// test ends, so a test can reach the checks after the password.
func AcceptEveryPassword(t testing.TB) {
	verifyPassword = func(string, string) (bool, error) { return true, nil }
	t.Cleanup(func() { verifyPassword = VerifyPassword })
}

// DummySecret is the TOTP secret an unknown email's code is checked against.
func (s *Sessions) DummySecret() ([]byte, error) { return s.codes.Open(s.dummySecret) }

// FailuresKey is the Redis key holding email's recent sign-in failures, for
// a Lockout whose keys start with prefix.
func FailuresKey(prefix, email string) string {
	return (&Lockout{prefix: prefix}).key("failures", email)
}
