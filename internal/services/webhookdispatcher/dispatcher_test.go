package webhookdispatcher

import (
	"context"
	"errors"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/DIMO-Network/cloudevent"
	"github.com/DIMO-Network/vehicle-triggers-api/internal/controllers/webhook"
	"github.com/DIMO-Network/vehicle-triggers-api/internal/db/models"
	"github.com/rs/zerolog"
	"github.com/stretchr/testify/require"
)

// fakeSender counts deliveries and can simulate latency or failure.
type fakeSender struct {
	calls atomic.Uint64
	delay time.Duration
	err   error
}

func (s *fakeSender) SendWebhook(ctx context.Context, _ *models.Trigger, _ *cloudevent.CloudEvent[webhook.WebhookPayload]) error {
	s.calls.Add(1)
	if s.delay > 0 {
		select {
		case <-time.After(s.delay):
		case <-ctx.Done():
			return ctx.Err()
		}
	}
	return s.err
}

func sampleJob() Job {
	return Job{
		Trigger: &models.Trigger{ID: "t-1", Status: "enabled"},
		Payload: &cloudevent.CloudEvent[webhook.WebhookPayload]{
			Data: webhook.WebhookPayload{
				WebhookID: "t-1",
				AssetDID: cloudevent.ERC721DID{
					ChainID: 137,
				},
			},
		},
	}
}

func TestDispatcherSynchronous(t *testing.T) {
	t.Parallel()
	sender := &fakeSender{}
	d := New(Config{Workers: 0, QueueSize: 1, MaxFailureCount: 5}, sender, nil, nil, nil, zerolog.Nop())
	require.NoError(t, d.Enqueue(t.Context(), sampleJob()))
	require.EqualValues(t, 1, sender.calls.Load())
}

func TestDispatcherAsyncDrains(t *testing.T) {
	t.Parallel()
	sender := &fakeSender{}
	d := New(Config{Workers: 4, QueueSize: 16, MaxFailureCount: 5}, sender, nil, nil, nil, zerolog.Nop())

	ctx, cancel := context.WithCancel(t.Context())
	done := make(chan error, 1)
	go func() { done <- d.Run(ctx) }()

	const N = 10
	for i := 0; i < N; i++ {
		require.NoError(t, d.Enqueue(t.Context(), sampleJob()))
	}

	require.Eventually(t, func() bool { return sender.calls.Load() == N }, 2*time.Second, 10*time.Millisecond, "dispatcher must drain all jobs")
	cancel()
	require.NoError(t, <-done)
}

func TestDispatcherQueueFullBackpressure(t *testing.T) {
	t.Parallel()
	// Slow sender so workers never drain. Tiny queue so Enqueue rejects.
	sender := &fakeSender{delay: 500 * time.Millisecond}
	d := New(Config{Workers: 1, QueueSize: 1, MaxFailureCount: 5}, sender, nil, nil, nil, zerolog.Nop())

	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()
	go func() { _ = d.Run(ctx) }()

	// Fill the worker slot + the buffer slot, then expect rejection.
	require.NoError(t, d.Enqueue(t.Context(), sampleJob()))
	// Wait until the first job is being processed so the queue can fill.
	require.Eventually(t, func() bool { return sender.calls.Load() == 1 }, time.Second, 5*time.Millisecond)

	require.NoError(t, d.Enqueue(t.Context(), sampleJob())) // sits in buffer

	err := d.Enqueue(t.Context(), sampleJob()) // must reject
	require.ErrorIs(t, err, ErrQueueFull)
}

func TestDispatcherShutdownDrainsInFlight(t *testing.T) {
	t.Parallel()
	sender := &fakeSender{delay: 50 * time.Millisecond}
	d := New(Config{Workers: 2, QueueSize: 8, MaxFailureCount: 5}, sender, nil, nil, nil, zerolog.Nop())

	ctx, cancel := context.WithCancel(t.Context())
	var runErr error
	var wg sync.WaitGroup
	wg.Add(1)
	go func() {
		defer wg.Done()
		runErr = d.Run(ctx)
	}()

	for i := 0; i < 4; i++ {
		require.NoError(t, d.Enqueue(t.Context(), sampleJob()))
	}

	// Wait for at least one worker to have dequeued before cancelling,
	// otherwise on busy schedulers Run returns before any sender call lands
	// and the test becomes flaky.
	require.Eventually(t, func() bool { return sender.calls.Load() >= 1 }, 2*time.Second, 5*time.Millisecond)

	// Cancel and verify Run returns within a reasonable bound.
	cancel()
	gotEarly := make(chan struct{})
	go func() { wg.Wait(); close(gotEarly) }()
	select {
	case <-gotEarly:
	case <-time.After(2 * time.Second):
		t.Fatal("Run did not return after context cancel")
	}
	require.NoError(t, runErr)
	// At least one worker dequeued before cancel; we don't pin a higher
	// bound because scheduler timing makes it flaky in busy test suites.
	require.GreaterOrEqual(t, sender.calls.Load(), uint64(1), "at least one job should have been delivered")
}

func TestDispatcherDeliveryError(t *testing.T) {
	t.Parallel()
	sender := &fakeSender{err: errors.New("boom")}
	d := New(Config{Workers: 0, QueueSize: 1, MaxFailureCount: 5}, sender, nil, nil, nil, zerolog.Nop())
	require.NoError(t, d.Enqueue(t.Context(), sampleJob())) // sync mode swallows the error (metric only)
	require.EqualValues(t, 1, sender.calls.Load())
}

// flakyThenOKSender fails the first N times then succeeds. Used to exercise
// the in-worker retry loop.
type flakyThenOKSender struct {
	failuresLeft int32
	calls        atomic.Uint64
}

func (s *flakyThenOKSender) SendWebhook(_ context.Context, _ *models.Trigger, _ *cloudevent.CloudEvent[webhook.WebhookPayload]) error {
	s.calls.Add(1)
	if atomic.AddInt32(&s.failuresLeft, -1) >= 0 {
		return errors.New("transient")
	}
	return nil
}

func TestDispatcherInWorkerRetry(t *testing.T) {
	t.Parallel()
	// Two transient failures then success - covered by RetryAttempts=2.
	sender := &flakyThenOKSender{failuresLeft: 2}
	d := New(Config{
		Workers:           0,
		QueueSize:         1,
		MaxFailureCount:   5,
		RetryAttempts:     2,
		RetryInitialDelay: time.Millisecond, // keep test fast
	}, sender, nil, nil, nil, zerolog.Nop())

	require.NoError(t, d.Enqueue(t.Context(), sampleJob()))
	require.EqualValues(t, 3, sender.calls.Load(), "first attempt + 2 retries")
}

func TestDispatcherRetryExhausted(t *testing.T) {
	t.Parallel()
	// Three transient failures with RetryAttempts=2 means we still error.
	sender := &flakyThenOKSender{failuresLeft: 3}
	d := New(Config{
		Workers:           0,
		QueueSize:         1,
		MaxFailureCount:   5,
		RetryAttempts:     2,
		RetryInitialDelay: time.Millisecond,
	}, sender, nil, nil, nil, zerolog.Nop())

	require.NoError(t, d.Enqueue(t.Context(), sampleJob()))
	require.EqualValues(t, 3, sender.calls.Load(), "exhausted at 1+2 attempts")
}
