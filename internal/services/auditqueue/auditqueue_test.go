package auditqueue

import (
	"context"
	"errors"
	"sync/atomic"
	"testing"
	"time"

	"github.com/rs/zerolog"
	"github.com/stretchr/testify/require"
)

type fakePublisher struct {
	calls atomic.Uint64
	delay time.Duration
	err   error
}

func (f *fakePublisher) PublishTriggerFired(ctx context.Context, _ string, _ []byte) error {
	f.calls.Add(1)
	if f.delay > 0 {
		select {
		case <-time.After(f.delay):
		case <-ctx.Done():
			return ctx.Err()
		}
	}
	return f.err
}

func TestQueueDrains(t *testing.T) {
	t.Parallel()
	p := &fakePublisher{}
	q := New(Config{Workers: 2, Buffer: 64, PublishTimeout: time.Second}, p, zerolog.Nop())
	ctx, cancel := context.WithCancel(t.Context())
	go func() { _ = q.Run(ctx) }()

	for i := 0; i < 32; i++ {
		require.True(t, q.Submit(Entry{DevLicense: "0xdead", Record: []byte("{}")}))
	}
	require.Eventually(t, func() bool { return p.calls.Load() == 32 }, 2*time.Second, 10*time.Millisecond)
	cancel()
}

func TestQueueDropsWhenFull(t *testing.T) {
	t.Parallel()
	p := &fakePublisher{delay: 200 * time.Millisecond}
	q := New(Config{Workers: 1, Buffer: 1, PublishTimeout: time.Second}, p, zerolog.Nop())
	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()
	go func() { _ = q.Run(ctx) }()

	// Fill the worker slot + 1 buffer slot, then expect drop.
	require.True(t, q.Submit(Entry{}))
	require.Eventually(t, func() bool { return p.calls.Load() == 1 }, time.Second, 5*time.Millisecond)
	require.True(t, q.Submit(Entry{}))
	require.False(t, q.Submit(Entry{}), "should drop on full buffer")
}

func TestQueuePublishTriggerFiredAdapter(t *testing.T) {
	t.Parallel()
	p := &fakePublisher{}
	q := New(Config{Workers: 1, Buffer: 4, PublishTimeout: time.Second}, p, zerolog.Nop())
	ctx, cancel := context.WithCancel(t.Context())
	go func() { _ = q.Run(ctx) }()

	// AuditPublisher adapter must return nil even on full queue, because
	// dispatcher cannot do anything with the error.
	require.NoError(t, q.PublishTriggerFired(t.Context(), "0xa", []byte("{}")))
	require.Eventually(t, func() bool { return p.calls.Load() == 1 }, time.Second, 5*time.Millisecond)
	cancel()
}

func TestQueuePublishErrorObservable(t *testing.T) {
	t.Parallel()
	p := &fakePublisher{err: errors.New("boom")}
	q := New(Config{Workers: 1, Buffer: 4, PublishTimeout: time.Second}, p, zerolog.Nop())
	ctx, cancel := context.WithCancel(t.Context())
	go func() { _ = q.Run(ctx) }()

	require.True(t, q.Submit(Entry{}))
	require.Eventually(t, func() bool { return p.calls.Load() == 1 }, time.Second, 5*time.Millisecond)
	cancel()
	// Errors don't surface in Submit return; ops sees them via
	// vehicle_triggers_audit_publish_errors_total.
}
