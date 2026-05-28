package e2e_test

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/DIMO-Network/vehicle-triggers-api/internal/config"
	vtnats "github.com/DIMO-Network/vehicle-triggers-api/internal/nats"
	nc "github.com/nats-io/nats.go"
	"github.com/nats-io/nats.go/jetstream"
	"github.com/rs/zerolog"
	"github.com/stretchr/testify/require"
)

// TestNATSDLQ verifies that a message whose handler keeps failing is parked in
// the DLQ stream (with diagnostic headers) after exceeding MaxDeliver, rather
// than redelivering forever or silently dropping.
func TestNATSDLQ(t *testing.T) {
	t.Parallel()
	natsServer := setupMockNATSServer(t)
	t.Cleanup(func() { _ = natsServer.Close() })

	suffix := time.Now().Format("150405000")
	cfg := config.NATSSettings{
		Mode:               "exclusive",
		URL:                natsServer.URL(),
		Name:               "vt-dlq-" + suffix,
		SignalsStream:      "DLQ_SIGNALS_" + suffix,
		EventsStream:       "DLQ_EVENTS_" + suffix,
		AuditStream:        "DLQ_AUDIT_" + suffix,
		DLQStream:          "DLQ_DLQ_" + suffix,
		SignalsSubject:     "dimo.signals.>",
		EventsSubject:      "dimo.events.>",
		AuditSubject:       "dimo.trigger.fired.>",
		DLQSubject:         "dimo.dlq.>",
		SignalsDurable:     "dlq-sig-" + suffix,
		EventsDurable:      "dlq-evt-" + suffix,
		WebhooksBucket:     "dlq_wh_" + suffix,
		TriggerStateBucket:  "dlq_state_" + suffix,
		SignalHistoryBucket: "dlq_hist_" + suffix,
		StreamReplicas:     1,
		SignalsMaxAge:      time.Minute,
		EventsMaxAge:       time.Minute,
		AuditMaxAge:        time.Minute,
		DLQMaxAge:          time.Minute,
		AckWait:            time.Second, // short so redelivery is fast
		MaxDeliver:         2,           // first attempt + 1 retry, then DLQ
		MaxAckPending:      100,
		FetchBatch:         10,
		TriggerStateTTL:    time.Minute,
	}

	client, err := vtnats.Connect(t.Context(), cfg, zerolog.New(zerolog.NewTestWriter(t)))
	require.NoError(t, err)
	t.Cleanup(func() { _ = client.Close() })
	require.NoError(t, client.EnsureStreams(t.Context()))
	require.NoError(t, client.EnsureBuckets(t.Context()))

	cons, err := client.EnsureConsumer(t.Context(), vtnats.ConsumerSpec{
		Stream:         cfg.SignalsStream,
		Durable:        cfg.SignalsDurable,
		FilterSubjects: []string{vtnats.AllSignalsFilter()},
		DeliverPolicy:  jetstream.DeliverAllPolicy,
		AckWait:        cfg.AckWait,
		MaxDeliver:     cfg.MaxDeliver,
		MaxAckPending:  cfg.MaxAckPending,
	})
	require.NoError(t, err)

	// Handler that always fails: simulates an unparseable payload or a
	// permanently broken downstream.
	alwaysFail := func(_ context.Context, _ []byte) error {
		return errors.New("synthetic permanent failure")
	}

	ctx, cancel := context.WithCancel(t.Context())
	t.Cleanup(cancel)
	go func() {
		if err := client.PullLoop(ctx, cons, 4, alwaysFail); err != nil && !errors.Is(err, context.Canceled) {
			t.Errorf("pull loop: %v", err)
		}
	}()

	// Publish one signal. It will fail, retry once, then be DLQ'd.
	_, err = client.Publish(t.Context(), vtnats.SignalSubject("speed"), []byte(`{"garbage":true}`))
	require.NoError(t, err)

	// Tap the DLQ stream and assert the message lands with diagnostic headers.
	conn, err := nc.Connect(natsServer.URL())
	require.NoError(t, err)
	t.Cleanup(func() { conn.Close() })
	js, err := jetstream.New(conn)
	require.NoError(t, err)

	dlqCons, err := js.CreateOrUpdateConsumer(t.Context(), cfg.DLQStream, jetstream.ConsumerConfig{
		Durable:       "dlq-tap-" + suffix,
		AckPolicy:     jetstream.AckExplicitPolicy,
		DeliverPolicy: jetstream.DeliverAllPolicy,
		FilterSubject: cfg.DLQSubject,
	})
	require.NoError(t, err)

	msgs, err := dlqCons.Fetch(1, jetstream.FetchMaxWait(15*time.Second))
	require.NoError(t, err)
	var got jetstream.Msg
	for m := range msgs.Messages() {
		got = m
		require.NoError(t, m.Ack())
		break
	}
	require.NoError(t, msgs.Error())
	require.NotNil(t, got, "expected a message in the DLQ stream")

	require.Equal(t, "dimo.dlq.dimo.signals.speed", got.Subject())
	require.Equal(t, "dimo.signals.speed", got.Headers().Get("X-Original-Subject"))
	require.Contains(t, got.Headers().Get("X-Failure-Reason"), "synthetic permanent failure")
	require.Equal(t, `{"garbage":true}`, string(got.Data()))
}
