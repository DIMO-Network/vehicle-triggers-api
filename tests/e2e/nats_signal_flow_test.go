package e2e_test

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"math/big"
	"net/http"
	"os"
	"testing"
	"time"

	"github.com/DIMO-Network/cloudevent"
	"github.com/DIMO-Network/model-garage/pkg/vss"
	"github.com/DIMO-Network/vehicle-triggers-api/internal/app"
	"github.com/DIMO-Network/vehicle-triggers-api/internal/controllers/webhook"
	vtnats "github.com/DIMO-Network/vehicle-triggers-api/internal/nats"
	"github.com/DIMO-Network/vehicle-triggers-api/internal/services/triggersrepo"
	"github.com/DIMO-Network/vehicle-triggers-api/tests"
	"github.com/ethereum/go-ethereum/common"
	nc "github.com/nats-io/nats.go"
	"github.com/nats-io/nats.go/jetstream"
	"github.com/rs/zerolog"
	"github.com/stretchr/testify/require"
)

// TestNATSSignalFlow exercises the full NATS-primary ingest path: webhook
// registered via API, vehicle subscribed, signal published directly to
// JetStream, webhook delivered. Mirrors TestSignalWebhookFlow but uses the
// NATS pipeline instead of Kafka.
func TestNATSSignalFlow(t *testing.T) {
	t.Parallel()
	tc := GetTestServices(t)

	natsServer := setupMockNATSServer(t)
	t.Cleanup(func() { _ = natsServer.Close() })

	devAddress := tests.RandomAddr(t)
	settingsCopy := tc.Settings
	// Kafka topics still set so the Kafka consumers can start; they will be
	// idle bridges in NATS-primary mode.
	settingsCopy.DeviceEventsTopic = "test-event-topic" + devAddress.String()
	settingsCopy.DeviceSignalsTopic = "test-signal-topic" + devAddress.String()
	settingsCopy.NATS.Mode = "primary"
	settingsCopy.NATS.URL = natsServer.URL()
	settingsCopy.NATS.SignalsStream = "TEST_SIGNALS_" + devAddress.Hex()[2:10]
	settingsCopy.NATS.EventsStream = "TEST_EVENTS_" + devAddress.Hex()[2:10]
	settingsCopy.NATS.AuditStream = "TEST_AUDIT_" + devAddress.Hex()[2:10]
	settingsCopy.NATS.SignalsDurable = "test-sig-" + devAddress.Hex()[2:10]
	settingsCopy.NATS.EventsDurable = "test-evt-" + devAddress.Hex()[2:10]
	settingsCopy.NATS.WebhooksBucket = "tb_wh_" + devAddress.Hex()[2:10]
	settingsCopy.NATS.SignalIndexBucket = "tb_idx_" + devAddress.Hex()[2:10]
	settingsCopy.NATS.TriggerStateBucket = "tb_state_" + devAddress.Hex()[2:10]
	settingsCopy.NATS.Name = "vt-test-" + devAddress.Hex()[2:10]
	settingsCopy.NATS.StreamReplicas = 1
	settingsCopy.NATS.SignalsMaxAge = time.Minute
	settingsCopy.NATS.EventsMaxAge = time.Minute
	settingsCopy.NATS.AuditMaxAge = time.Minute
	settingsCopy.NATS.AckWait = 5 * time.Second
	settingsCopy.NATS.MaxDeliver = 3
	settingsCopy.NATS.MaxAckPending = 1000
	settingsCopy.NATS.FetchBatch = 50
	settingsCopy.NATS.TriggerStateTTL = time.Minute

	servers, err := app.CreateServers(t.Context(), &settingsCopy, zerolog.New(os.Stdout))
	require.NoError(t, err)
	require.NotNil(t, servers.NATSClient, "expected NATS client to be wired")
	require.NotNil(t, servers.NATSSignalConsumer, "expected NATS signals consumer to be created in primary mode")

	// Start Kafka consumers (they'll bridge to NATS since Mode=primary)
	// and the NATS pull loop driving evaluation.
	ctx, cancel := context.WithCancel(t.Context())
	t.Cleanup(cancel)

	go func() {
		if err := servers.SignalConsumer.Start(ctx); err != nil && !errors.Is(err, context.Canceled) {
			t.Errorf("signal consumer: %v", err)
		}
	}()
	go func() {
		if err := servers.EventConsumer.Start(ctx); err != nil && !errors.Is(err, context.Canceled) {
			t.Errorf("event consumer: %v", err)
		}
	}()
	go func() {
		if err := servers.NATSClient.PullLoop(ctx, servers.NATSSignalConsumer, settingsCopy.MaxInFlight, servers.NATSListener.HandleSignalPayload); err != nil && !errors.Is(err, context.Canceled) {
			t.Errorf("nats pull loop: %v", err)
		}
	}()
	t.Cleanup(func() {
		_ = servers.SignalConsumer.Stop(context.Background())
		_ = servers.EventConsumer.Stop(context.Background())
		shutdownCtx, c := context.WithTimeout(context.Background(), 5*time.Second)
		defer c()
		_ = servers.NATSClient.Shutdown(shutdownCtx)
	})

	// Webhook receiver
	webhookReceiver := NewWebhookReceiver()
	t.Cleanup(webhookReceiver.Close)

	// Register webhook via the API
	regBody, err := json.Marshal(webhook.RegisterWebhookRequest{
		Service:           triggersrepo.ServiceSignal,
		MetricName:        "vss.speed",
		Condition:         "valueNumber > 20",
		CoolDownPeriod:    0,
		Description:       "NATS e2e: speed > 20",
		TargetURL:         webhookReceiver.URL(),
		Status:            triggersrepo.StatusEnabled,
		VerificationToken: "test-verification-token",
	})
	require.NoError(t, err)

	authToken, err := tc.Auth.CreateToken(t, devAddress)
	require.NoError(t, err)
	require.NoError(t, tc.Identity.SetRequestResponse(
		fmt.Sprintf(`{"query":"\n\tquery($clientId: Address){\n\t\tdeveloperLicense(by: { clientId: $clientId }) {\n\t\t\tclientId\n\t\t}\n\t}","variables":{"clientId":"%s"}}`, devAddress.String()),
		map[string]any{"data": map[string]any{"developerLicense": map[string]any{"clientId": devAddress.String()}}},
	))

	req, err := http.NewRequestWithContext(t.Context(), "POST", "/v1/webhooks", bytes.NewBuffer(regBody))
	require.NoError(t, err)
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "Bearer "+authToken)
	resp, err := servers.Application.Test(req, -1)
	require.NoError(t, err)
	body, _ := io.ReadAll(resp.Body)
	require.Equal(t, http.StatusCreated, resp.StatusCode, string(body))

	var regResp map[string]any
	require.NoError(t, json.Unmarshal(body, &regResp))
	webhookID, _ := regResp["id"].(string)
	require.NotEmpty(t, webhookID)

	// Subscribe vehicle
	assetDid := cloudevent.ERC721DID{
		ChainID:         137,
		ContractAddress: common.HexToAddress("0xbA5738a18d83D41847dfFbDC6101d37C69c9B0cF"),
		TokenID:         big.NewInt(99001),
	}
	tc.TokenExchange.SetAccessCheckReturn(devAddress.String(), true)
	subReq, err := http.NewRequestWithContext(t.Context(), "POST", fmt.Sprintf("/v1/webhooks/%s/subscribe/%s", webhookID, assetDid.String()), nil)
	require.NoError(t, err)
	subReq.Header.Set("Authorization", "Bearer "+authToken)
	subResp, err := servers.Application.Test(subReq, -1)
	require.NoError(t, err)
	require.Equal(t, http.StatusCreated, subResp.StatusCode)
	time.Sleep(1 * time.Second) // let webhook cache refresh

	// Publish signal directly to JetStream on the production subject.
	conn, err := nc.Connect(natsServer.URL())
	require.NoError(t, err)
	t.Cleanup(func() { conn.Close() })
	js, err := jetstream.New(conn)
	require.NoError(t, err)

	publishSignal(t, js, assetDid, "speed", 35.0)

	require.True(t, webhookReceiver.WaitForCall(10*time.Second), "webhook not called after NATS publish")
	calls := webhookReceiver.GetReceivedCalls()
	require.Len(t, calls, 1)

	var payload map[string]any
	require.NoError(t, json.Unmarshal([]byte(calls[0].Body), &payload))
	data := payload["data"].(map[string]any)
	signal := data["signal"].(map[string]any)
	require.Equal(t, "speed", signal["name"].(string))
	require.Equal(t, float64(35), signal["value"].(float64))
}

func publishSignal(t *testing.T, js jetstream.JetStream, did cloudevent.ERC721DID, name string, value float64) {
	t.Helper()
	now := time.Now().UTC()
	hdr := cloudevent.CloudEventHeader{
		SpecVersion:     "1.0",
		Type:            "dimo.signal",
		Source:          "nats-e2e",
		Subject:         did.String(),
		ID:              fmt.Sprintf("e2e-%d", now.UnixNano()),
		Time:            now,
		DataContentType: "application/json",
		Producer:        "nats-e2e/synthetic",
	}
	sig := vss.Signal{
		CloudEventHeader: hdr,
		Data:             vss.SignalData{Timestamp: now, Name: name, ValueNumber: value},
	}
	ce := vss.PackSignals(hdr, []vss.Signal{sig})
	payload, err := json.Marshal(ce)
	require.NoError(t, err)
	_, err = js.Publish(t.Context(), vtnats.SignalSubject(name), payload)
	require.NoError(t, err)
}
