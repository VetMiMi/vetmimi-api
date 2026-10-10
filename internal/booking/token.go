package booking

import "crypto/rand"

const seedBytes = 32

// NewSeed returns the random seed a management link is derived from. The
// seed is stored, so the worker can render the link; the token never is, only
// its tokens.Hash.
func NewSeed() ([]byte, error) {
	seed := make([]byte, seedBytes)
	if _, err := rand.Read(seed); err != nil {
		return nil, err
	}
	return seed, nil
}
