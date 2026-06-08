package configaudit

import (
	"context"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestNoopPublishSucceeds(t *testing.T) {
	t.Parallel()
	require.NoError(t, Noop{}.Publish(context.Background(), Event{Op: OpWebhookCreate}))
}

func TestSanitize(t *testing.T) {
	t.Parallel()
	cases := map[string]string{
		"abc-123":   "abc-123",
		"a.b":       "a_b",
		"with*":     "with_",
		"angle>":    "angle_",
		"line\nend": "line_end",
		"":          "_",
	}
	for in, want := range cases {
		require.Equal(t, want, sanitize(in), "sanitize(%q)", in)
	}
}

func TestNewReturnsNoopOnNilClient(t *testing.T) {
	t.Parallel()
	p := New(nil)
	_, ok := p.(Noop)
	require.True(t, ok, "nil client should produce a Noop publisher")
}
