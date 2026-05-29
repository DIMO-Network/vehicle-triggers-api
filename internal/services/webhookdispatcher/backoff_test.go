package webhookdispatcher

import (
	"context"
	"errors"
	"sync"
	"testing"
	"time"

	"github.com/DIMO-Network/cloudevent"
	"github.com/DIMO-Network/vehicle-triggers-api/internal/controllers/webhook"
	"github.com/DIMO-Network/vehicle-triggers-api/internal/db/models"
	vtnats "github.com/DIMO-Network/vehicle-triggers-api/internal/nats"
	"github.com/DIMO-Network/vehicle-triggers-api/internal/services/webhooksender"
	"github.com/rs/zerolog"
	"github.com/stretchr/testify/require"
)

// timestampedSender records the exact moment each delivery attempt arrived
// so we can verify the backoff schedule matches what's documented in code
// comments and the PROD_HARDENING_V2 description for item F.
type timestampedSender struct {
	mu    sync.Mutex
	times []time.Time
	err   error
}

func (s *timestampedSender) SendWebhook(_ context.Context, _ *models.Trigger, _ *cloudevent.CloudEvent[webhook.WebhookPayload]) error {
	s.mu.Lock()
	s.times = append(s.times, time.Now())
	s.mu.Unlock()
	return s.err
}

// TestBackoffScheduleMatchesContract pins the exponential backoff schedule.
// If anyone tunes the multiplier without thinking, this test catches it
// and forces a doc update.
func TestBackoffScheduleMatchesContract(t *testing.T) {
	t.Parallel()
	sender := &timestampedSender{err: errors.New("transient")}
	d := New(Config{
		Workers:           0,
		QueueSize:         1,
		MaxFailureCount:   5,
		RetryAttempts:     2,
		RetryInitialDelay: 100 * time.Millisecond,
	}, sender, nil, nil, nil, zerolog.Nop())

	require.NoError(t, d.Enqueue(context.Background(), Job{
		Trigger: &models.Trigger{ID: "t", Status: "enabled"},
		Payload: &cloudevent.CloudEvent[webhook.WebhookPayload]{},
	}))

	sender.mu.Lock()
	defer sender.mu.Unlock()
	require.Len(t, sender.times, 3, "first attempt + 2 retries")

	// Expected gaps: 100ms initial, 500ms second (×5 multiplier).
	gap1 := sender.times[1].Sub(sender.times[0])
	gap2 := sender.times[2].Sub(sender.times[1])
	require.InDelta(t, float64(100*time.Millisecond), float64(gap1), float64(40*time.Millisecond),
		"first retry should fire ~100ms after initial attempt, got %s", gap1)
	require.InDelta(t, float64(500*time.Millisecond), float64(gap2), float64(80*time.Millisecond),
		"second retry should fire ~500ms after first retry (5x multiplier), got %s", gap2)
}

// TestErrQueueFullIsBackpressure pins the integration with the NATS
// PullLoop's backpressure classifier. Without this, an Enqueue rejection
// would be treated as poison and consume MaxDeliver budget.
func TestErrQueueFullIsBackpressure(t *testing.T) {
	t.Parallel()
	require.True(t, errors.Is(ErrQueueFull, vtnats.ErrBackpressure),
		"ErrQueueFull must wrap nats.ErrBackpressure so PullLoop uses the long-delay nak path")
}

// TestPermanentErrorSkipsRetries pins the 4xx classifier: a permanent
// receiver error (wrapped as webhooksender.ErrPermanent) must short-circuit
// the in-worker retry loop. Without this, we burn RetryAttempts + per-host
// rate-limit tokens on a receiver that will never accept the payload.
func TestPermanentErrorSkipsRetries(t *testing.T) {
	t.Parallel()
	sender := &timestampedSender{err: webhooksender.ErrPermanent}
	d := New(Config{
		Workers:           0,
		QueueSize:         1,
		MaxFailureCount:   5,
		RetryAttempts:     3,
		RetryInitialDelay: 100 * time.Millisecond,
	}, sender, nil, nil, nil, zerolog.Nop())

	require.NoError(t, d.Enqueue(context.Background(), Job{
		Trigger: &models.Trigger{ID: "t", Status: "enabled"},
		Payload: &cloudevent.CloudEvent[webhook.WebhookPayload]{},
	}))

	sender.mu.Lock()
	defer sender.mu.Unlock()
	require.Len(t, sender.times, 1, "permanent error must NOT retry; got %d attempts", len(sender.times))
}
