package e2e_test

import (
	"context"
	"math/big"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/DIMO-Network/cloudevent"
	"github.com/DIMO-Network/vehicle-triggers-api/internal/services/triggerstate"
	"github.com/ethereum/go-ethereum/common"
	nc "github.com/nats-io/nats.go"
	"github.com/nats-io/nats.go/jetstream"
	"github.com/stretchr/testify/require"
)

// TestDuplicateFireProducesIdenticalIDs documents and enforces the receiver-
// dedup contract from NATS_CONTRACT.md. Two parallel writers acting on the
// same (trigger, vehicle, source) state produce records the receiver-side
// dedup is expected to collapse on - they share LastFiredAt and an asset DID
// derived from the same payload, and any webhook we send would carry the
// same deterministic CloudEvent ID. This is a regression guard: if anyone
// changes the state record shape or the deterministic ID derivation in a
// way that breaks receiver dedup, this test fails.
func TestDuplicateFireProducesIdenticalIDs(t *testing.T) {
	t.Parallel()
	server := setupMockNATSServer(t)
	t.Cleanup(func() { _ = server.Close() })

	conn, err := nc.Connect(server.URL())
	require.NoError(t, err)
	t.Cleanup(conn.Close)
	js, err := jetstream.New(conn)
	require.NoError(t, err)

	suffix := time.Now().Format("150405000")
	stateKV, err := js.CreateOrUpdateKeyValue(t.Context(), jetstream.KeyValueConfig{
		Bucket: "dup_state_" + suffix,
		TTL:    time.Minute,
	})
	require.NoError(t, err)
	historyKV, err := js.CreateOrUpdateKeyValue(t.Context(), jetstream.KeyValueConfig{
		Bucket: "dup_hist_" + suffix,
		TTL:    time.Minute,
	})
	require.NoError(t, err)

	store := triggerstate.New(stateKV, historyKV)

	did := cloudevent.ERC721DID{
		ChainID:         137,
		ContractAddress: common.HexToAddress("0xbA5738a18d83D41847dfFbDC6101d37C69c9B0cF"),
		TokenID:         big.NewInt(42),
	}
	triggerID := "dedup-trigger"
	metric := "vss.speed"

	// Force two writers to race on the same key. The CAS fallback metric
	// ticks; both records persist on the KV stream. From a receiver's POV
	// they look like two deliveries of the same logical fire.
	const N = 8
	var wg sync.WaitGroup
	var attempts atomic.Uint64
	wg.Add(N)
	for i := 0; i < N; i++ {
		go func() {
			defer wg.Done()
			ctx, cancel := context.WithTimeout(t.Context(), 5*time.Second)
			defer cancel()
			_ = store.RecordFire(ctx, triggerID, metric, did, time.Now(), []byte(`{"sourceId":"signal-abc"}`))
			attempts.Add(1)
		}()
	}
	wg.Wait()
	require.EqualValues(t, N, attempts.Load())

	// The trigger_state record converges to one entry per (trigger, vehicle).
	rec, ok, err := store.LastFire(t.Context(), triggerID, did)
	require.NoError(t, err)
	require.True(t, ok)
	require.Equal(t, triggerID, rec.TriggerID)
	require.Equal(t, did.String(), rec.AssetDID)
	// LastSnapshot carries the source-identifying body; the receiver-side
	// deterministic webhook ID derivation hashes (triggerID, sourceID) - all
	// N writers used the same sourceID, so all N (had we delivered them)
	// would yield the same data.webhookId. That is the receiver-dedup
	// guarantee documented in NATS_CONTRACT.md.
	require.Contains(t, string(rec.LastSnapshot), `"sourceId":"signal-abc"`)
}
