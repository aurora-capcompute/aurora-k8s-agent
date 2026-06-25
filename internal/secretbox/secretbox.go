// Package secretbox is the shared AES-GCM seal/open used for at-rest secrets:
// a nonce is generated per message and prepended to the ciphertext, matching the
// format the state stores already use. It backs the channel "inPlaceEncrypted"
// secret variant and the seal-secret helper.
package secretbox

import (
	"crypto/aes"
	"crypto/cipher"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"errors"
	"fmt"
	"io"
)

// DeriveKey turns a configured value into a 32-byte AES key: a base64-encoded
// 32-byte value is used directly, otherwise the value is SHA-256'd. This mirrors
// the agent's existing state-key derivation so one configured key works for both.
func DeriveKey(value string) []byte {
	if decoded, err := base64.StdEncoding.DecodeString(value); err == nil && len(decoded) == 32 {
		return decoded
	}
	sum := sha256.Sum256([]byte(value))
	return sum[:]
}

func newAEAD(key []byte) (cipher.AEAD, error) {
	if len(key) != 32 {
		return nil, errors.New("secretbox key must be exactly 32 bytes")
	}
	block, err := aes.NewCipher(key)
	if err != nil {
		return nil, err
	}
	return cipher.NewGCM(block)
}

// Seal encrypts plaintext with key and returns nonce‖ciphertext.
func Seal(key, plaintext []byte) ([]byte, error) {
	aead, err := newAEAD(key)
	if err != nil {
		return nil, err
	}
	nonce := make([]byte, aead.NonceSize())
	if _, err := io.ReadFull(rand.Reader, nonce); err != nil {
		return nil, err
	}
	return aead.Seal(nonce, nonce, plaintext, nil), nil
}

// Open reverses Seal: it splits the nonce prefix and decrypts. A wrong key or
// tampered ciphertext fails the GCM auth tag.
func Open(key, ciphertext []byte) ([]byte, error) {
	aead, err := newAEAD(key)
	if err != nil {
		return nil, err
	}
	size := aead.NonceSize()
	if len(ciphertext) < size {
		return nil, fmt.Errorf("ciphertext too short: %d < nonce %d", len(ciphertext), size)
	}
	return aead.Open(nil, ciphertext[:size], ciphertext[size:], nil)
}

// SealBase64 / OpenBase64 are the string-encoded forms used in CRDs.
func SealBase64(key, plaintext []byte) (string, error) {
	sealed, err := Seal(key, plaintext)
	if err != nil {
		return "", err
	}
	return base64.StdEncoding.EncodeToString(sealed), nil
}

func OpenBase64(key []byte, encoded string) ([]byte, error) {
	raw, err := base64.StdEncoding.DecodeString(encoded)
	if err != nil {
		return nil, fmt.Errorf("decode base64: %w", err)
	}
	return Open(key, raw)
}
