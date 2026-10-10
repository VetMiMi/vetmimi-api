// Package secretbox encrypts small secrets at rest, such as TOTP secrets and
// OAuth tokens, with AES-256-GCM. A sealed value is the random nonce followed
// by the ciphertext.
package secretbox

import (
	"crypto/aes"
	"crypto/cipher"
	"crypto/rand"
	"errors"
)

const keyBytes = 32 // AES-256

type Box struct {
	aead cipher.AEAD
}

// New takes TOTP_ENCRYPTION_KEY, which must be 32 bytes.
func New(key []byte) (*Box, error) {
	if len(key) != keyBytes {
		return nil, errors.New("secretbox: the encryption key must be 32 bytes")
	}
	block, err := aes.NewCipher(key)
	if err != nil {
		return nil, err
	}
	aead, err := cipher.NewGCM(block)
	if err != nil {
		return nil, err
	}
	return &Box{aead: aead}, nil
}

func (b *Box) Seal(plain []byte) ([]byte, error) {
	nonce := make([]byte, b.aead.NonceSize())
	if _, err := rand.Read(nonce); err != nil {
		return nil, err
	}
	return b.aead.Seal(nonce, nonce, plain, nil), nil
}

// Open fails if sealed was altered or sealed under another key.
func (b *Box) Open(sealed []byte) ([]byte, error) {
	n := b.aead.NonceSize()
	if len(sealed) < n {
		return nil, errors.New("secretbox: sealed value is too short")
	}
	return b.aead.Open(nil, sealed[:n], sealed[n:], nil)
}
