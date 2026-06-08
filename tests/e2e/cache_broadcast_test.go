package e2e_test

import (
	"context"
	"sync/atomic"
	"testing"
	"time"

	"github.com/DIMO-Network/vehicle-triggers-api/internal/services/cachebroadcast"
	nc "github.com/nats-io/nats.go"
	"github.com/rs/zerolog"
	"github.com/stretchr/testify/require"
)

type recordingRefresher struct {
	count atomic.Uint64
}

func (r *recordingRefresher) InvalidateTrigger(string) {}

func (r *recordingRefresher) ScheduleRefreshSilent(context.Context) {
	r.count.Add(1)
}

// TestCacheBroadcast verifies the publish->subscribe loop: a Notify call on
// one connection fires the Subscribe callback on a second connection,
// triggering the refresher. The receiver side calls ScheduleRefreshSilent,
// not ScheduleRefresh, so notifications don't echo into infinite republishes.
func TestCacheBroadcast(t *testing.T) {
	t.Parallel()
	server := setupMockNATSServer(t)
	t.Cleanup(func() { _ = server.Close() })

	pubConn, err := nc.Connect(server.URL())
	require.NoError(t, err)
	t.Cleanup(pubConn.Close)
	subConn, err := nc.Connect(server.URL())
	require.NoError(t, err)
	t.Cleanup(subConn.Close)

	notifier := cachebroadcast.NewNATSNotifier(pubConn, zerolog.Nop())
	refresher := &recordingRefresher{}
	sub, err := cachebroadcast.Subscribe(subConn, t.Context(), refresher, zerolog.Nop())
	require.NoError(t, err)
	t.Cleanup(func() { _ = sub.Unsubscribe() })
	// Flush both sides so the server registers the subscription before we
	// publish; without this the publish can race ahead of subscription setup.
	require.NoError(t, subConn.Flush())

	require.NoError(t, notifier.Notify(t.Context(), "wh-1", "update"))
	require.NoError(t, pubConn.Flush())

	require.Eventually(t, func() bool { return refresher.count.Load() == 1 }, 2*time.Second, 10*time.Millisecond, "refresher should receive the notification")
}
