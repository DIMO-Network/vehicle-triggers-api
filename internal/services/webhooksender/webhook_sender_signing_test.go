package webhooksender

import (
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"strconv"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

func TestSignatureHeaders(t *testing.T) {
	t.Parallel()

	t.Run("empty secret returns empty", func(t *testing.T) {
		ts, sig := signatureHeaders("", []byte("body"))
		require.Empty(t, ts)
		require.Empty(t, sig)
	})

	t.Run("signature is HMAC-SHA256 over timestamp + . + body", func(t *testing.T) {
		secret := "test-secret"
		body := []byte(`{"hello":"world"}`)
		ts, sig := signatureHeaders(secret, body)

		// Timestamp parses as a unix epoch second within +/- 5s of now.
		got, err := strconv.ParseInt(ts, 10, 64)
		require.NoError(t, err)
		require.InDelta(t, time.Now().Unix(), got, 5)

		// Recompute and compare.
		mac := hmac.New(sha256.New, []byte(secret))
		mac.Write([]byte(ts))
		mac.Write([]byte{'.'})
		mac.Write(body)
		expected := hex.EncodeToString(mac.Sum(nil))
		require.Equal(t, expected, sig)
	})

	t.Run("different secrets produce different sigs", func(t *testing.T) {
		body := []byte("same body")
		_, a := signatureHeaders("secret-a", body)
		_, b := signatureHeaders("secret-b", body)
		require.NotEqual(t, a, b)
	})
}
