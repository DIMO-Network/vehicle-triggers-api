package secrets

import (
	"crypto/rand"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestPlaintextRoundtrip(t *testing.T) {
	t.Parallel()
	c := Plaintext{}
	ct, err := c.Encrypt("hello")
	require.NoError(t, err)
	require.Equal(t, "hello", ct)
	pt, err := c.Decrypt(ct)
	require.NoError(t, err)
	require.Equal(t, "hello", pt)
}

func TestAESGCMRequiresCorrectKeySize(t *testing.T) {
	t.Parallel()
	_, err := NewAESGCM(make([]byte, 16))
	require.Error(t, err)
}

func TestAESGCMRoundtrip(t *testing.T) {
	t.Parallel()
	key := make([]byte, 32)
	_, err := rand.Read(key)
	require.NoError(t, err)
	c, err := NewAESGCM(key)
	require.NoError(t, err)

	secret := "the rain in spain"
	ct, err := c.Encrypt(secret)
	require.NoError(t, err)
	require.NotEqual(t, secret, ct, "ciphertext must differ from plaintext")

	pt, err := c.Decrypt(ct)
	require.NoError(t, err)
	require.Equal(t, secret, pt)
}

func TestAESGCMDecryptLegacyPlaintext(t *testing.T) {
	t.Parallel()
	key := make([]byte, 32)
	_, err := rand.Read(key)
	require.NoError(t, err)
	c, err := NewAESGCM(key)
	require.NoError(t, err)

	// A raw plaintext secret from before encryption was enabled doesn't
	// look like our nonce||ciphertext format. We pass it through unchanged.
	pt, err := c.Decrypt("plain-old-secret")
	require.NoError(t, err)
	require.Equal(t, "plain-old-secret", pt)
}

func TestAESGCMDecryptWrongKey(t *testing.T) {
	t.Parallel()
	k1 := make([]byte, 32)
	k2 := make([]byte, 32)
	_, _ = rand.Read(k1)
	_, _ = rand.Read(k2)
	c1, _ := NewAESGCM(k1)
	c2, _ := NewAESGCM(k2)

	ct, err := c1.Encrypt("secret")
	require.NoError(t, err)
	_, err = c2.Decrypt(ct)
	require.ErrorContains(t, err, "auth tag mismatch")
}
