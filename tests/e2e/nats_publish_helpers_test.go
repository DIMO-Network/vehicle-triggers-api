package e2e_test

import (
	"context"
	"encoding/json"
	"testing"
	"time"

	"github.com/DIMO-Network/cloudevent"
	"github.com/DIMO-Network/model-garage/pkg/vss"
	"github.com/DIMO-Network/vehicle-triggers-api/internal/nats"
	"github.com/nats-io/nats.go/jetstream"
	"github.com/stretchr/testify/require"
)

// publishSignal wraps a single signal into a CloudEvent and publishes one
// message on the per-signal subject the production consumers filter on. Used
// by the exclusive-mode e2e tests to feed the NATS pipeline directly,
// bypassing the deprecated Kafka entry.
func publishSignal(t *testing.T, js jetstream.JetStream, assetDID cloudevent.ERC721DID, signalName string, value float64) {
	t.Helper()
	ce := vss.PackSignals(cloudevent.CloudEventHeader{
		Subject:  assetDID.String(),
		Source:   "test-source",
		Producer: "test-producer",
		ID:       "test-event-" + signalName,
	}, []vss.Signal{
		{
			CloudEventHeader: cloudevent.CloudEventHeader{
				Subject:  assetDID.String(),
				Source:   "test-source",
				Producer: "test-producer",
			},
			Data: vss.SignalData{
				Timestamp:   time.Now(),
				Name:        signalName,
				ValueNumber: value,
				CloudEventID: "test-event-" + signalName,
			},
		},
	})
	body, err := json.Marshal(ce)
	require.NoError(t, err)
	ctx, cancel := context.WithTimeout(t.Context(), 5*time.Second)
	defer cancel()
	_, err = js.Publish(ctx, nats.SignalSubject(signalName), body)
	require.NoError(t, err)
}
