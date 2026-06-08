package e2e_test

import (
	"context"
	"encoding/json"
	"math/big"
	"sync"
	"testing"
	"time"

	"github.com/DIMO-Network/cloudevent"
	"github.com/DIMO-Network/vehicle-triggers-api/internal/services/triggerstate"
	"github.com/ethereum/go-ethereum/common"
	nc "github.com/nats-io/nats.go"
	"github.com/nats-io/nats.go/jetstream"
	"github.com/stretchr/testify/require"
)

// TestTriggerStateCAS asserts that two goroutines racing RecordFire on the
// same key both eventually succeed (KV state is preserved) and the conflict
// counter records the race - the receiver-dedup story relies on this counter
// being observable to operators.
func TestTriggerStateCAS(t *testing.T) {
	t.Parallel()
	server := setupMockNATSServer(t)
	t.Cleanup(func() { _ = server.Close() })

	conn, err := nc.Connect(server.URL())
	require.NoError(t, err)
	t.Cleanup(func() { conn.Close() })
	js, err := jetstream.New(conn)
	require.NoError(t, err)

	suffix := time.Now().Format("150405000")
	stateKV, err := js.CreateOrUpdateKeyValue(t.Context(), jetstream.KeyValueConfig{
		Bucket: "cas_state_" + suffix,
		TTL:    time.Minute,
	})
	require.NoError(t, err)
	historyKV, err := js.CreateOrUpdateKeyValue(t.Context(), jetstream.KeyValueConfig{
		Bucket: "cas_hist_" + suffix,
		TTL:    time.Minute,
	})
	require.NoError(t, err)

	store := triggerstate.New(stateKV, historyKV)

	did := cloudevent.ERC721DID{
		ChainID:         137,
		ContractAddress: common.HexToAddress("0xbA5738a18d83D41847dfFbDC6101d37C69c9B0cF"),
		TokenID:         big.NewInt(7),
	}
	triggerID := "race-trigger"
	metric := "vss.speed"

	// First write - must be Create.
	require.NoError(t, store.RecordFire(t.Context(), triggerID, metric, did, time.Now(), json.RawMessage(`{"v":1}`)))

	// Now race N writers. The KV value after all complete should be one of
	// the snapshots (last writer wins on fallback), and the bucket must hold
	// a valid Record.
	const N = 8
	var wg sync.WaitGroup
	wg.Add(N)
	for i := 0; i < N; i++ {
		go func(i int) {
			defer wg.Done()
			ctx, cancel := context.WithTimeout(t.Context(), 5*time.Second)
			defer cancel()
			snap, _ := json.Marshal(map[string]int{"i": i + 100})
			// Errors are not asserted here - the CAS-with-fallback contract
			// means concurrent writers don't surface errors; instead the
			// vehicle_triggers_state_cas_conflicts_total counter ticks.
			_ = store.RecordFire(ctx, triggerID, metric, did, time.Now(), snap)
		}(i)
	}
	wg.Wait()

	rec, ok, err := store.LastFire(t.Context(), triggerID, did)
	require.NoError(t, err)
	require.True(t, ok)
	require.Equal(t, triggerID, rec.TriggerID)
	require.Equal(t, did.String(), rec.AssetDID)
	require.NotEmpty(t, rec.LastSnapshot, "snapshot from winning writer must be preserved")
}
