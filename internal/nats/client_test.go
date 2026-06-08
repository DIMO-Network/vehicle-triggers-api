package nats_test

import (
	"context"
	"os"
	"testing"
	"time"

	"github.com/DIMO-Network/vehicle-triggers-api/internal/config"
	vtnats "github.com/DIMO-Network/vehicle-triggers-api/internal/nats"
	"github.com/nats-io/nats.go/jetstream"
	"github.com/rs/zerolog"
	"github.com/stretchr/testify/require"
)

// testNATSURL returns a URL from NATS_TEST_URL or skips the test.
// Run locally with: NATS_TEST_URL=nats://localhost:4222 go test ./internal/nats/...
func testNATSURL(t *testing.T) string {
	t.Helper()
	u := os.Getenv("NATS_TEST_URL")
	if u == "" {
		t.Skip("NATS_TEST_URL not set")
	}
	return u
}

func testSettings(url, prefix string) config.NATSSettings {
	suffix := itoa(time.Now().UnixNano())
	return config.NATSSettings{
		URL:                url,
		Name:               "vt-test",
		SignalsStream:      "T_" + prefix + "_SIG_" + suffix,
		EventsStream:       "T_" + prefix + "_EVT_" + suffix,
		AuditStream:        "T_" + prefix + "_AUD_" + suffix,
		DLQStream:          "T_" + prefix + "_DLQ_" + suffix,
		ConfigAuditStream:  "T_" + prefix + "_CFG_" + suffix,
		SignalsSubject:     "t." + prefix + "." + suffix + ".signals.>",
		EventsSubject:      "t." + prefix + "." + suffix + ".events.>",
		AuditSubject:       "t." + prefix + "." + suffix + ".audit.>",
		DLQSubject:         "t." + prefix + "." + suffix + ".dlq.>",
		ConfigAuditSubject: "t." + prefix + "." + suffix + ".cfg.>",
		SignalsDurable:     "sig-" + suffix,
		EventsDurable:      "evt-" + suffix,
		StreamReplicas:     1,
		SignalsMaxAge:      time.Minute,
		EventsMaxAge:       time.Minute,
		AuditMaxAge:        time.Minute,
		FetchBatch:         10,
		AckWait:            5 * time.Second,
		MaxDeliver:         3,
		MaxAckPending:      100,
		FilterSubjectCap:   128,
		TriggerStateBucket:  "tb_state_" + suffix,
		SignalHistoryBucket: "tb_hist_" + suffix,
		TriggerStateTTL:    time.Minute,
	}
}

func itoa(n int64) string {
	const digits = "0123456789"
	if n == 0 {
		return "0"
	}
	neg := n < 0
	if neg {
		n = -n
	}
	var buf [20]byte
	i := len(buf)
	for n > 0 {
		i--
		buf[i] = digits[n%10]
		n /= 10
	}
	if neg {
		i--
		buf[i] = '-'
	}
	return string(buf[i:])
}

func TestConnectAndProvision(t *testing.T) {
	url := testNATSURL(t)
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	cfg := testSettings(url, "prov")

	c, err := vtnats.Connect(ctx, cfg, zerolog.Nop())
	require.NoError(t, err)
	t.Cleanup(func() { _ = c.Close() })

	require.NoError(t, c.EnsureStreams(ctx))
	require.NoError(t, c.EnsureBuckets(ctx))

	// trigger_state bucket is reachable.
	tsKV, err := c.TriggerState(ctx)
	require.NoError(t, err)
	_, err = tsKV.PutString(ctx, "probe", "ok")
	require.NoError(t, err)
	got, err := tsKV.Get(ctx, "probe")
	require.NoError(t, err)
	require.Equal(t, "ok", string(got.Value()))

	// Cleanup: delete streams we created.
	t.Cleanup(func() {
		_ = c.JS.DeleteStream(ctx, cfg.SignalsStream)
		_ = c.JS.DeleteStream(ctx, cfg.EventsStream)
		_ = c.JS.DeleteStream(ctx, cfg.AuditStream)
		_ = c.JS.DeleteKeyValue(ctx, cfg.TriggerStateBucket)
		_ = c.JS.DeleteKeyValue(ctx, cfg.SignalHistoryBucket)
	})
}

func TestPublishSubscribeRoundtrip(t *testing.T) {
	url := testNATSURL(t)
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	cfg := testSettings(url, "rt")

	c, err := vtnats.Connect(ctx, cfg, zerolog.Nop())
	require.NoError(t, err)
	t.Cleanup(func() { _ = c.Close() })

	require.NoError(t, c.EnsureStreams(ctx))
	t.Cleanup(func() {
		_ = c.JS.DeleteStream(ctx, cfg.SignalsStream)
		_ = c.JS.DeleteStream(ctx, cfg.EventsStream)
		_ = c.JS.DeleteStream(ctx, cfg.AuditStream)
	})

	prefix := cfg.SignalsSubject[:len(cfg.SignalsSubject)-1] // strip trailing ">"
	subject := prefix + "speed"
	filter := prefix + "speed"
	seq, err := c.Publish(ctx, subject, []byte(`{"hello":"world"}`))
	require.NoError(t, err)
	require.Greater(t, seq, uint64(0))

	cons, err := c.EnsureConsumer(ctx, vtnats.ConsumerSpec{
		Stream:         cfg.SignalsStream,
		Durable:        cfg.SignalsDurable,
		FilterSubjects: []string{filter},
		DeliverPolicy:  jetstream.DeliverAllPolicy,
	})
	require.NoError(t, err)

	msgs, err := cons.Fetch(1, jetstream.FetchMaxWait(3*time.Second))
	require.NoError(t, err)
	var got []byte
	for m := range msgs.Messages() {
		got = m.Data()
		require.NoError(t, m.Ack())
		break
	}
	require.NoError(t, msgs.Error())
	require.Equal(t, `{"hello":"world"}`, string(got))
}
