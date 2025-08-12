package e2e_test

import (
	"bytes"
	"encoding/json"
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
	"github.com/DIMO-Network/vehicle-triggers-api/internal/services/triggersrepo"
	"github.com/ethereum/go-ethereum/common"
	"github.com/rs/zerolog"
	"github.com/stretchr/testify/require"
)

func TestWebhookFlow(t *testing.T) {
	t.Parallel()
	tc := GetTestServices(t)

	// Create the main application
	fiberApp, err := app.CreateServers(t.Context(), &tc.Settings, zerolog.New(os.Stdout))
	require.NoError(t, err)

	// Create a webhook receiver
	webhookReceiver := NewWebhookReceiver()
	defer webhookReceiver.Close()

	// Create a developer address for testing
	devAddress := common.HexToAddress("0x1234567890123456789012345678901234567890")

	// Step 1: Create a webhook
	t.Log("Step 1: Creating webhook")
	webhookPayload := webhook.RegisterWebhookRequest{
		Service:           triggersrepo.ServiceSignal,
		MetricName:        "speed",
		Condition:         "valueNumber > 20",
		CoolDownPeriod:    10,
		Description:       "Alert when vehicle speed exceeds 20 kph",
		TargetURL:         webhookReceiver.URL(),
		Status:            triggersrepo.StatusEnabled,
		VerificationToken: "test-verification-token",
	}

	webhookBody, err := json.Marshal(webhookPayload)
	require.NoError(t, err)

	// Create auth token for the request
	authToken, err := tc.Auth.CreateToken(t, devAddress)
	require.NoError(t, err)

	// add dev license to identity api
	err = tc.Identity.SetRequestResponse(
		`{"query":"\n\tquery($clientId: Address){\n\t\tdeveloperLicense(by: { clientId: $clientId }) {\n\t\t\tclientId\n\t\t}\n\t}","variables":{"clientId":"0x1234567890123456789012345678901234567890"}}`,
		map[string]any{
			"data": map[string]any{
				"developerLicense": map[string]any{
					"clientId": devAddress.String(),
				},
			},
		})
	require.NoError(t, err)

	// Make the webhook creation request
	req, err := http.NewRequestWithContext(
		t.Context(),
		"POST",
		"/v1/webhooks",
		bytes.NewBuffer(webhookBody),
	)
	require.NoError(t, err)

	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "Bearer "+authToken)

	resp, err := fiberApp.Test(req, -1)
	require.NoError(t, err)
	body, err := io.ReadAll(resp.Body)
	require.NoError(t, err)
	bodyStr := string(body)
	require.Equal(t, http.StatusCreated, resp.StatusCode, bodyStr)

	// Parse the response to get the webhook ID
	webhookResponse := map[string]any{}
	err = json.Unmarshal(body, &webhookResponse)
	require.NoError(t, err)

	webhookID, ok := webhookResponse["id"].(string)
	require.True(t, ok, "Expected webhook ID in response")
	require.NotEmpty(t, webhookID)

	t.Logf("Created webhook with ID: %s", webhookID)

	// Step 2: Subscribe a vehicle to the webhook
	t.Log("Step 2: Subscribing vehicle to webhook")

	// Use a test vehicle token ID
	assetDid := cloudevent.ERC721DID{
		ChainID:         137,
		ContractAddress: common.HexToAddress("0xbA5738a18d83D41847dfFbDC6101d37C69c9B0cF"),
		TokenID:         big.NewInt(12345),
	}

	// Set up token exchange API mock to return permissions for this vehicle
	tc.TokenExchange.SetAccessCheckReturn(devAddress.String(), true)

	// Make the subscription request
	subscribeURL := fmt.Sprintf("/v1/webhooks/%s/subscribe/%s", webhookID, assetDid.String())
	req, err = http.NewRequestWithContext(
		t.Context(),
		"POST",
		subscribeURL,
		nil,
	)
	require.NoError(t, err)

	req.Header.Set("Authorization", "Bearer "+authToken)

	resp, err = fiberApp.Test(req, -1)
	require.NoError(t, err)
	require.Equal(t, http.StatusCreated, resp.StatusCode)

	t.Logf("Subscribed vehicle %s to webhook %s waiting for webhook to be updated", assetDid.String(), webhookID)
	time.Sleep(1 * time.Second)

	// Step 3: Send a signal to Kafka to trigger the webhook
	t.Log("Step 3: Sending signal to Kafka")
	signalPayload := vss.Signal{
		TokenID:      12345, // Same as assetDID.TokenID
		Timestamp:    time.Now(),
		Name:         "speed",
		ValueNumber:  25.0, // Above the 20 threshold to trigger the webhook
		ValueString:  "",
		Source:       "test-source",
		Producer:     "test-producer",
		CloudEventID: "test-event-id",
	}

	err = tc.Kafka.PushJSONToTopic(tc.Settings.DeviceSignalsTopic, signalPayload)
	require.NoError(t, err)

	t.Log("Signal sent to Kafka")

	// Step 4: Verify the webhook was called
	t.Log("Step 4: Verifying webhook was called")

	// Wait for the webhook to be called (with timeout)
	received := webhookReceiver.WaitForCall(10 * time.Second)
	require.True(t, received, "Webhook was not called within timeout")

	// Get the received calls
	calls := webhookReceiver.GetReceivedCalls()
	require.Len(t, calls, 1, "Expected exactly one webhook call")

	call := calls[0]
	t.Logf("Webhook called with method: %s, URL: %s", call.Method, call.URL)
	t.Logf("Webhook body: %s", call.Body)

	// Verify the webhook call details
	require.Equal(t, "POST", call.Method)
	require.Equal(t, "/", call.URL) // The webhook receiver URL path

	// Parse the webhook body to verify it contains the signal data
	var webhookBodyData map[string]any
	err = json.Unmarshal([]byte(call.Body), &webhookBodyData)
	require.NoError(t, err)

	// Verify the webhook contains the expected signal data
	require.Contains(t, webhookBodyData, "data")
	data := webhookBodyData["data"].(map[string]any)
	require.Contains(t, data, "signal")
	signal := data["signal"].(map[string]any)
	require.Equal(t, float64(25), signal["value"].(float64))
	require.Equal(t, "speed", signal["name"].(string))

	fmt.Printf("cloudEvent: \n%s", call.Body)
}
