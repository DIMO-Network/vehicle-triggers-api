// Package secrets is a tiny encryption seam for the per-trigger HMAC
// signing secret. It defines a Cipher interface so the repository can write
// ciphertext to Postgres instead of plaintext, and an implementation backed
// by AES-256-GCM with a key supplied by the operator via env / KMS.
//
// We do not depend on AWS KMS directly because:
//   - The service is cloud-portable; tying to KMS would force operators on
//     other clouds to swap the package out.
//   - A single AES-GCM key from env works for the deploy size we ship.
//     Operators who need rotation can layer in their own KMS data-key
//     wrapping on top by implementing this interface in a custom package.
//
// The default Cipher when no key is configured is Plaintext, which writes
// and reads the raw secret. That preserves backwards compatibility with the
// existing schema while letting deployments opt in to encryption by setting
// SIGNING_SECRET_KEY_HEX to a 64-char hex string (32 bytes).
package secrets

import (
	"crypto/aes"
	"crypto/cipher"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
)

// Cipher encrypts/decrypts the per-trigger signing secret at the storage
// boundary. Encrypt returns a value safe to persist; Decrypt returns the
// original. Both must be idempotent over the round-trip:
// Decrypt(Encrypt(x)) == x.
type Cipher interface {
	Encrypt(plaintext string) (string, error)
	Decrypt(stored string) (string, error)
}

// Plaintext is the no-op cipher used when SIGNING_SECRET_KEY_HEX is unset.
// Existing rows continue to round-trip unchanged.
type Plaintext struct{}

func (Plaintext) Encrypt(s string) (string, error) { return s, nil }
func (Plaintext) Decrypt(s string) (string, error) { return s, nil }

// AESGCM encrypts each secret with a fresh 96-bit nonce, prefixed to the
// ciphertext: storage layout is base16(nonce || ciphertext). The output is
// distinguishable from plaintext by length (32 hex prefix for nonce) and by
// the leading byte being valid hex, so we don't need a magic prefix.
type AESGCM struct {
	aead cipher.AEAD
}

// NewAESGCM builds an AES-256-GCM cipher from a 32-byte key. Pass the key
// as raw bytes - the caller is responsible for fetching it from env/KMS and
// hex-decoding if needed.
func NewAESGCM(key []byte) (*AESGCM, error) {
	if len(key) != 32 {
		return nil, fmt.Errorf("secrets: AES-256-GCM requires a 32-byte key, got %d", len(key))
	}
	block, err := aes.NewCipher(key)
	if err != nil {
		return nil, fmt.Errorf("secrets: aes.NewCipher: %w", err)
	}
	aead, err := cipher.NewGCM(block)
	if err != nil {
		return nil, fmt.Errorf("secrets: cipher.NewGCM: %w", err)
	}
	return &AESGCM{aead: aead}, nil
}

// Encrypt produces a hex-encoded nonce||ciphertext blob.
func (c *AESGCM) Encrypt(plaintext string) (string, error) {
	nonce := make([]byte, c.aead.NonceSize())
	if _, err := rand.Read(nonce); err != nil {
		return "", fmt.Errorf("secrets: nonce: %w", err)
	}
	out := c.aead.Seal(nonce, nonce, []byte(plaintext), nil)
	return hex.EncodeToString(out), nil
}

// Decrypt accepts either a hex-encoded encrypted blob or, for backwards
// compatibility with rows persisted before encryption was enabled, a raw
// plaintext secret. Heuristic: if the input hex-decodes AND the decoded
// length is at least nonce + 16 (auth tag), treat as encrypted; otherwise
// return as-is.
func (c *AESGCM) Decrypt(stored string) (string, error) {
	raw, err := hex.DecodeString(stored)
	if err != nil || len(raw) < c.aead.NonceSize()+16 {
		// Doesn't look like our encrypted format. Treat as legacy plaintext.
		return stored, nil
	}
	nonce, ct := raw[:c.aead.NonceSize()], raw[c.aead.NonceSize():]
	pt, err := c.aead.Open(nil, nonce, ct, nil)
	if err != nil {
		// Looked like ciphertext but auth failed. Could be legacy plaintext
		// that happens to hex-decode; surface a clear error rather than
		// silently mishandling.
		return "", errors.New("secrets: decrypt failed (auth tag mismatch); is SIGNING_SECRET_KEY_HEX wrong?")
	}
	return string(pt), nil
}
