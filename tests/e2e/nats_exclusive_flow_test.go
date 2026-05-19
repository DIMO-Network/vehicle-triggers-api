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
	"github.com/DIMO-Network/vehicle-triggers-api/internal/app"
	"github.com/DIMO-Network/vehicle-triggers-api/internal/controllers/webhook"
	"github.com/DIMO-Network/vehicle-triggers-api/internal/services/triggersrepo"
	"github.com/DIMO-Network/vehicle-triggers-api/tests"
	"github.com/ethereum/go-ethereum/common"
	nc "github.com/nats-io/nats.go"
	"github.com/nats-io/nats.go/jetstream"
	"github.com/rs/zerolog"
	"github.com/stretchr/testify/require"
)

// TestNATSExclusiveFlow verifies the Kafka-free target state: NATS_MODE=exclusive
// means the service starts without Kafka consumers and runs evaluation purely
// from JetStream. We also assert that an audit record lands on the audit
// stream so billing downstream gets the fire.
func TestNATSExclusiveFlow(t *testing.T) {
	t.Parallel()
	tc := GetTestServices(t)

	natsServer := setupMockNATSServer(t)
	t.Cleanup(func() { _ = natsServer.Close() })

	devAddress := tests.RandomAddr(t)
	settingsCopy := tc.Settings
	// Wipe Kafka topics to make sure nothing accidentally falls back to it.
	settingsCopy.KafkaBrokers = ""
	settingsCopy.DeviceSignalsTopic = ""
	settingsCopy.DeviceEventsTopic = ""

	settingsCopy.NATS.Mode = "exclusive"
	settingsCopy.NATS.URL = natsServer.URL()
	suffix := devAddress.Hex()[2:10]
	settingsCopy.NATS.SignalsStream = "EX_SIGNALS_" + suffix
	settingsCopy.NATS.EventsStream = "EX_EVENTS_" + suffix
	settingsCopy.NATS.AuditStream = "EX_AUDIT_" + suffix
	settingsCopy.NATS.DLQStream = "EX_DLQ_" + suffix
	settingsCopy.NATS.SignalsSubject = "dimo.signals.>"
	settingsCopy.NATS.EventsSubject = "dimo.events.>"
	settingsCopy.NATS.AuditSubject = "dimo.trigger.fired.>"
	settingsCopy.NATS.DLQSubject = "dimo.dlq.>"
	settingsCopy.NATS.SignalsDurable = "ex-sig-" + suffix
	settingsCopy.NATS.EventsDurable = "ex-evt-" + suffix
	settingsCopy.NATS.WebhooksBucket = "ex_wh_" + suffix
	settingsCopy.NATS.SignalIndexBucket = "ex_idx_" + suffix
	settingsCopy.NATS.TriggerStateBucket = "ex_state_" + suffix
	settingsCopy.NATS.Name = "vt-ex-" + suffix
	settingsCopy.NATS.StreamReplicas = 1
	settingsCopy.NATS.SignalsMaxAge = time.Minute
	settingsCopy.NATS.EventsMaxAge = time.Minute
	settingsCopy.NATS.AuditMaxAge = time.Minute
	settingsCopy.NATS.DLQMaxAge = time.Minute
	settingsCopy.NATS.AckWait = 5 * time.Second
	settingsCopy.NATS.MaxDeliver = 10
	settingsCopy.NATS.MaxAckPending = 1000
	settingsCopy.NATS.FetchBatch = 50
	settingsCopy.NATS.TriggerStateTTL = time.Minute

	servers, err := app.CreateServers(t.Context(), &settingsCopy, zerolog.New(os.Stdout))
	require.NoError(t, err)
	require.Nil(t, servers.SignalConsumer, "Kafka signal consumer should not be created in exclusive mode")
	require.Nil(t, servers.EventConsumer, "Kafka event consumer should not be created in exclusive mode")
	require.NotNil(t, servers.NATSClient)
	require.NotNil(t, servers.NATSSignalConsumer)

	ctx, cancel := context.WithCancel(t.Context())
	t.Cleanup(cancel)
	go func() {
		if err := servers.NATSClient.PullLoop(ctx, servers.NATSSignalConsumer, settingsCopy.MaxInFlight, servers.NATSListener.HandleSignalPayload); err != nil && !errors.Is(err, context.Canceled) {
			t.Errorf("nats pull loop: %v", err)
		}
	}()
	t.Cleanup(func() {
		shutdownCtx, c := context.WithTimeout(context.Background(), 5*time.Second)
		defer c()
		_ = servers.NATSClient.Shutdown(shutdownCtx)
	})

	receiver := NewWebhookReceiver()
	t.Cleanup(receiver.Close)

	authToken, err := tc.Auth.CreateToken(t, devAddress)
	require.NoError(t, err)
	require.NoError(t, tc.Identity.SetRequestResponse(
		fmt.Sprintf(`{"query":"\n\tquery($clientId: Address){\n\t\tdeveloperLicense(by: { clientId: $clientId }) {\n\t\t\tclientId\n\t\t}\n\t}","variables":{"clientId":"%s"}}`, devAddress.String()),
		map[string]any{"data": map[string]any{"developerLicense": map[string]any{"clientId": devAddress.String()}}},
	))

	regBody, _ := json.Marshal(webhook.RegisterWebhookRequest{
		Service:           triggersrepo.ServiceSignal,
		MetricName:        "vss.speed",
		Condition:         "valueNumber > 10",
		CoolDownPeriod:    0,
		Description:       "exclusive e2e: speed > 10",
		TargetURL:         receiver.URL(),
		Status:            triggersrepo.StatusEnabled,
		VerificationToken: "test-verification-token",
	})
	req, _ := http.NewRequestWithContext(t.Context(), "POST", "/v1/webhooks", bytes.NewBuffer(regBody))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "Bearer "+authToken)
	resp, err := servers.Application.Test(req, -1)
	require.NoError(t, err)
	body, _ := io.ReadAll(resp.Body)
	require.Equal(t, http.StatusCreated, resp.StatusCode, string(body))
	var regResp map[string]any
	_ = json.Unmarshal(body, &regResp)
	webhookID := regResp["id"].(string)

	assetDid := cloudevent.ERC721DID{
		ChainID:         137,
		ContractAddress: common.HexToAddress("0xbA5738a18d83D41847dfFbDC6101d37C69c9B0cF"),
		TokenID:         big.NewInt(99002),
	}
	tc.TokenExchange.SetAccessCheckReturn(devAddress.String(), true)
	subReq, _ := http.NewRequestWithContext(t.Context(), "POST", fmt.Sprintf("/v1/webhooks/%s/subscribe/%s", webhookID, assetDid.String()), nil)
	subReq.Header.Set("Authorization", "Bearer "+authToken)
	subResp, err := servers.Application.Test(subReq, -1)
	require.NoError(t, err)
	require.Equal(t, http.StatusCreated, subResp.StatusCode)
	time.Sleep(1 * time.Second)

	// Subscribe to audit stream first so the publish lands while we're listening.
	conn, err := nc.Connect(natsServer.URL())
	require.NoError(t, err)
	t.Cleanup(func() { conn.Close() })
	js, err := jetstream.New(conn)
	require.NoError(t, err)

	auditCons, err := js.CreateOrUpdateConsumer(t.Context(), settingsCopy.NATS.AuditStream, jetstream.ConsumerConfig{
		Durable:       "audit-tap-" + suffix,
		AckPolicy:     jetstream.AckExplicitPolicy,
		DeliverPolicy: jetstream.DeliverAllPolicy,
		FilterSubject: "dimo.trigger.fired.>",
	})
	require.NoError(t, err)

	publishSignal(t, js, assetDid, "speed", 42.0)

	require.True(t, receiver.WaitForCall(10*time.Second), "webhook not called in exclusive mode")
	calls := receiver.GetReceivedCalls()
	require.Len(t, calls, 1)

	// Verify audit stream caught the fire.
	auditMsgs, err := auditCons.Fetch(1, jetstream.FetchMaxWait(5*time.Second))
	require.NoError(t, err)
	var auditPayload map[string]any
	for m := range auditMsgs.Messages() {
		require.NoError(t, json.Unmarshal(m.Data(), &auditPayload))
		require.NoError(t, m.Ack())
		break
	}
	require.NoError(t, auditMsgs.Error())
	require.NotEmpty(t, auditPayload, "audit stream did not capture the fire")
	data := auditPayload["data"].(map[string]any)
	require.Equal(t, "vss.speed", data["metricName"].(string))
}
