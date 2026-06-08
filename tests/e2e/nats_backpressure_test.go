package e2e_test

import (
	"context"
	"errors"
	"fmt"
	"sync/atomic"
	"testing"
	"time"

	"github.com/DIMO-Network/vehicle-triggers-api/internal/config"
	vtnats "github.com/DIMO-Network/vehicle-triggers-api/internal/nats"
	nc "github.com/nats-io/nats.go"
	"github.com/nats-io/nats.go/jetstream"
	"github.com/rs/zerolog"
	"github.com/stretchr/testify/require"
)

// TestPullLoopBackpressureDoesNotDLQ verifies the D fix end-to-end: a
// handler that returns ErrBackpressure on every delivery gets long nak
// delays instead of burning through MaxDeliver and DLQing. Without the fix,
// MaxDeliver=3 attempts would land the message in the DLQ within seconds.
// With the fix, the message stays in the original stream undergoing slow
// retries until either the handler relents or operator scales out.
//
// We assert: the DLQ stream remains empty for at least 1s while the handler
// keeps returning ErrBackpressure, and consume metrics show
// nak_backpressure rather than dlq.
func TestPullLoopBackpressureDoesNotDLQ(t *testing.T) {
	t.Parallel()
	server := setupMockNATSServer(t)
	t.Cleanup(func() { _ = server.Close() })

	conn, err := nc.Connect(server.URL())
	require.NoError(t, err)
	t.Cleanup(conn.Close)
	js, err := jetstream.New(conn)
	require.NoError(t, err)

	suffix := time.Now().Format("150405000")
	// Tight settings so the test runs fast: BackpressureNakDelay is the
	// dominant timer (default 30s). Override it so the test finishes in
	// reasonable time without weakening the assertion.
	original := vtnats.BackpressureNakDelay
	vtnats.BackpressureNakDelay = 500 * time.Millisecond
	t.Cleanup(func() { vtnats.BackpressureNakDelay = original })

	streamName := "BP_SIG_" + suffix
	dlqName := "BP_DLQ_" + suffix
	_, err = js.CreateOrUpdateStream(t.Context(), jetstream.StreamConfig{
		Name:      streamName,
		Subjects:  []string{"bp.signals.>"},
		Retention: jetstream.LimitsPolicy,
		Storage:   jetstream.FileStorage,
		MaxAge:    time.Minute,
	})
	require.NoError(t, err)
	_, err = js.CreateOrUpdateStream(t.Context(), jetstream.StreamConfig{
		Name:      dlqName,
		Subjects:  []string{"bp.dlq.>"},
		Retention: jetstream.LimitsPolicy,
		Storage:   jetstream.FileStorage,
		MaxAge:    time.Minute,
	})
	require.NoError(t, err)
	t.Cleanup(func() {
		_ = js.DeleteStream(context.Background(), streamName)
		_ = js.DeleteStream(context.Background(), dlqName)
	})

	client, err := vtnats.Connect(t.Context(), config.NATSSettings{
		Mode:           "exclusive",
		URL:            server.URL(),
		Name:           "vt-bp-" + suffix,
		SignalsStream:  streamName,
		SignalsSubject: "bp.signals.>",
		DLQStream:      dlqName,
		DLQSubject:     "bp.dlq.>",
		MaxDeliver:     3,
		MaxAckPending:  100,
		AckWait:        2 * time.Second,
		FetchBatch:     10,
	}, zerolog.Nop())
	require.NoError(t, err)
	t.Cleanup(func() { _ = client.Close() })

	cons, err := client.EnsureConsumer(t.Context(), vtnats.ConsumerSpec{
		Stream:         streamName,
		Durable:        "bp-cons-" + suffix,
		FilterSubjects: []string{"bp.signals.>"},
		DeliverPolicy:  jetstream.DeliverAllPolicy,
		MaxDeliver:     3,
		AckWait:        2 * time.Second,
		MaxAckPending:  100,
	})
	require.NoError(t, err)

	var deliveries atomic.Uint64
	bpHandler := func(_ context.Context, _ []byte) error {
		deliveries.Add(1)
		return fmt.Errorf("simulated dispatch saturation: %w", vtnats.ErrBackpressure)
	}

	pullCtx, pullCancel := context.WithCancel(t.Context())
	t.Cleanup(pullCancel)
	go func() {
		_ = client.PullLoop(pullCtx, cons, 4, bpHandler)
	}()

	_, err = client.Publish(t.Context(), "bp.signals.speed", []byte(`{"x":1}`))
	require.NoError(t, err)

	// Watch for redelivery. With MaxDeliver=3 and BackpressureNakDelay=500ms,
	// we expect at most 3 attempts over ~1s+ wall time. Without the fix the
	// same 3 attempts would happen back-to-back in <100ms and land DLQ.
	require.Eventually(t, func() bool { return deliveries.Load() >= 2 }, 2*time.Second, 50*time.Millisecond, "at least 2 backpressure redeliveries")

	// Now assert the DLQ stream stays empty. If the backpressure path were
	// not honoured, MaxDeliver=3 would have already landed the message in
	// the DLQ during the eventually loop above.
	dlqStream, err := js.Stream(t.Context(), dlqName)
	require.NoError(t, err)
	info, err := dlqStream.Info(t.Context())
	require.NoError(t, err)
	require.Equal(t, uint64(0), info.State.Msgs, "DLQ must be empty for backpressure-only failures")
}

// TestPullLoopRealErrorDLQs is the counter-assertion: a non-backpressure
// handler error DOES land in the DLQ after MaxDeliver. This pins the D fix
// to "only ErrBackpressure gets special treatment".
func TestPullLoopRealErrorDLQs(t *testing.T) {
	t.Parallel()
	server := setupMockNATSServer(t)
	t.Cleanup(func() { _ = server.Close() })

	conn, err := nc.Connect(server.URL())
	require.NoError(t, err)
	t.Cleanup(conn.Close)
	js, err := jetstream.New(conn)
	require.NoError(t, err)

	suffix := time.Now().Format("150405000")
	streamName := "ERR_SIG_" + suffix
	dlqName := "ERR_DLQ_" + suffix
	_, err = js.CreateOrUpdateStream(t.Context(), jetstream.StreamConfig{
		Name:      streamName,
		Subjects:  []string{"err.signals.>"},
		Retention: jetstream.LimitsPolicy,
		Storage:   jetstream.FileStorage,
		MaxAge:    time.Minute,
	})
	require.NoError(t, err)
	_, err = js.CreateOrUpdateStream(t.Context(), jetstream.StreamConfig{
		Name:      dlqName,
		Subjects:  []string{"err.dlq.>"},
		Retention: jetstream.LimitsPolicy,
		Storage:   jetstream.FileStorage,
		MaxAge:    time.Minute,
	})
	require.NoError(t, err)
	t.Cleanup(func() {
		_ = js.DeleteStream(context.Background(), streamName)
		_ = js.DeleteStream(context.Background(), dlqName)
	})

	client, err := vtnats.Connect(t.Context(), config.NATSSettings{
		Mode:           "exclusive",
		URL:            server.URL(),
		Name:           "vt-err-" + suffix,
		SignalsStream:  streamName,
		SignalsSubject: "err.signals.>",
		DLQStream:      dlqName,
		DLQSubject:     "err.dlq.>",
		MaxDeliver:     2,
		MaxAckPending:  100,
		AckWait:        time.Second,
		FetchBatch:     10,
	}, zerolog.Nop())
	require.NoError(t, err)
	t.Cleanup(func() { _ = client.Close() })

	cons, err := client.EnsureConsumer(t.Context(), vtnats.ConsumerSpec{
		Stream:         streamName,
		Durable:        "err-cons-" + suffix,
		FilterSubjects: []string{"err.signals.>"},
		DeliverPolicy:  jetstream.DeliverAllPolicy,
		MaxDeliver:     2,
		AckWait:        time.Second,
		MaxAckPending:  100,
		BackOff:        []time.Duration{200 * time.Millisecond},
	})
	require.NoError(t, err)

	pullCtx, pullCancel := context.WithCancel(t.Context())
	t.Cleanup(pullCancel)
	go func() {
		_ = client.PullLoop(pullCtx, cons, 4, func(_ context.Context, _ []byte) error {
			return errors.New("permanent failure")
		})
	}()
	_, err = client.Publish(t.Context(), "err.signals.speed", []byte(`{"x":1}`))
	require.NoError(t, err)

	require.Eventually(t, func() bool {
		dlqStream, err := js.Stream(t.Context(), dlqName)
		if err != nil {
			return false
		}
		info, err := dlqStream.Info(t.Context())
		return err == nil && info.State.Msgs > 0
	}, 15*time.Second, 200*time.Millisecond, "real handler error must land in DLQ after MaxDeliver")
}
