//go:build load

// Build-tagged smoke load test. Runs the production NATS publish + pull-
// consumer path at a modest rate against a testcontainer JetStream and
// asserts no publish drops + clean consumer drain. Wired into CI as a
// separate job (go test -tags=load ./tests/e2e/...) so it doesn't inflate
// the default unit-test budget but still catches wiring regressions before
// they hit prod.
//
// Knobs are deliberately small (5s, ~300 msg/s) so the run is bounded; the
// real bench tool (cmd/triggers-bench) is what we use for capacity planning.
package e2e_test

import (
	"context"
	"errors"
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

func TestLoadSmokeNATS(t *testing.T) {
	t.Parallel()
	server := setupMockNATSServer(t)
	t.Cleanup(func() { _ = server.Close() })

	suffix := time.Now().Format("150405000")
	conn, err := nc.Connect(server.URL())
	require.NoError(t, err)
	t.Cleanup(conn.Close)
	js, err := jetstream.New(conn)
	require.NoError(t, err)

	streamName := "LOAD_SMOKE_" + suffix
	_, err = js.CreateOrUpdateStream(t.Context(), jetstream.StreamConfig{
		Name:      streamName,
		Subjects:  []string{"load.smoke.>"},
		Retention: jetstream.LimitsPolicy,
		Discard:   jetstream.DiscardOld,
		Storage:   jetstream.FileStorage,
		MaxAge:    5 * time.Minute,
	})
	require.NoError(t, err)
	t.Cleanup(func() { _ = js.DeleteStream(context.Background(), streamName) })

	cons, err := js.CreateOrUpdateConsumer(t.Context(), streamName, jetstream.ConsumerConfig{
		Durable:       "load-cons-" + suffix,
		AckPolicy:     jetstream.AckExplicitPolicy,
		DeliverPolicy: jetstream.DeliverAllPolicy,
		FilterSubject: "load.smoke.>",
		MaxAckPending: 10_000,
	})
	require.NoError(t, err)

	// Use the production Client + PullLoop so any wiring regression in those
	// surfaces here.
	client, err := vtnats.Connect(t.Context(), config.NATSSettings{
		Mode:       "exclusive",
		URL:        server.URL(),
		Name:       "load-smoke-" + suffix,
		FetchBatch: 100,
		MaxDeliver: 5,
	}, zerolog.Nop())
	require.NoError(t, err)
	t.Cleanup(func() { _ = client.Close() })

	var consumed, drops atomic.Uint64
	pullCtx, pullCancel := context.WithCancel(t.Context())
	t.Cleanup(pullCancel)
	go func() {
		_ = client.PullLoop(pullCtx, cons, 16, func(_ context.Context, _ []byte) error {
			consumed.Add(1)
			return nil
		})
	}()

	const rate = 300
	const duration = 5 * time.Second
	interval := time.Second / rate

	ticker := time.NewTicker(interval)
	pubCtx, pubCancel := context.WithTimeout(t.Context(), duration)
	defer pubCancel()
	var published atomic.Uint64
	for {
		stop := false
		select {
		case <-pubCtx.Done():
			ticker.Stop()
			stop = true
		case <-ticker.C:
			_, err := client.Publish(pubCtx, "load.smoke.speed", []byte(`{"v":1}`))
			if err != nil {
				if errors.Is(err, context.DeadlineExceeded) || errors.Is(err, context.Canceled) {
					ticker.Stop()
					stop = true
					break
				}
				drops.Add(1)
				continue
			}
			published.Add(1)
		}
		if stop {
			break
		}
	}

	deadline := time.Now().Add(10 * time.Second)
	for time.Now().Before(deadline) {
		if consumed.Load() >= published.Load() {
			break
		}
		time.Sleep(50 * time.Millisecond)
	}

	t.Logf("smoke load: published=%d consumed=%d drops=%d", published.Load(), consumed.Load(), drops.Load())
	require.Equal(t, uint64(0), drops.Load(), "publish drops are unacceptable at this rate")
	require.GreaterOrEqual(t, consumed.Load(), published.Load(), "consumer must drain all published messages")
}
