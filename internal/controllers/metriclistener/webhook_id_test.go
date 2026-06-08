package metriclistener

import (
	"testing"

	"github.com/stretchr/testify/require"
)

func TestWebhookID(t *testing.T) {
	t.Parallel()

	t.Run("deterministic across calls", func(t *testing.T) {
		a := webhookID("trig-123", "src-abc")
		b := webhookID("trig-123", "src-abc")
		require.Equal(t, a, b, "same (trigger, source) must produce the same id")
		require.Len(t, a, 32, "128-bit hex id expected")
	})

	t.Run("distinguishes triggers", func(t *testing.T) {
		require.NotEqual(t, webhookID("trig-a", "src"), webhookID("trig-b", "src"))
	})

	t.Run("distinguishes sources", func(t *testing.T) {
		require.NotEqual(t, webhookID("trig", "src-1"), webhookID("trig", "src-2"))
	})

	t.Run("falls back to uuid when source empty", func(t *testing.T) {
		a := webhookID("trig", "")
		b := webhookID("trig", "")
		require.NotEqual(t, a, b, "no source -> uuid -> non-deterministic")
		require.NotEmpty(t, a)
	})
}
