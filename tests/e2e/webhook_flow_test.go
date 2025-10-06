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
	"github.com/DIMO-Network/vehicle-triggers-api/internal/services/triggersrepo"
	"github.com/DIMO-Network/vehicle-triggers-api/tests"
	"github.com/ethereum/go-ethereum/common"
	"github.com/rs/zerolog"
	"github.com/stretchr/testify/require"
)

func TestSignalWebhookFlow(t *testing.T) {
	t.Parallel()
	tc := GetTestServices(t)

	// Create a developer address for testing
	devAddress := tests.RandomAddr(t)
	settingsCopy := tc.Settings
	settingsCopy.DeviceEventsTopic = "test-event-topic" + devAddress.String()
	settingsCopy.DeviceSignalsTopic = "test-signal-topic" + devAddress.String()

	// Create the main application
	servers, err := app.CreateServers(t.Context(), &settingsCopy, zerolog.New(os.Stdout))
	go func() {
		if err := servers.SignalConsumer.Start(t.Context()); err != nil {
			if !errors.Is(err, context.Canceled) {
				t.Errorf("failed to start signal consumer: %v", err)
			}
		}
	}()
	go func() {
		if err := servers.EventConsumer.Start(t.Context()); err != nil {
			if !errors.Is(err, context.Canceled) {
				t.Errorf("failed to start event consumer: %v", err)
			}
		}
	}()
	t.Cleanup(func() {
		_ = servers.SignalConsumer.Stop(t.Context())
		_ = servers.EventConsumer.Stop(t.Context())
	})
	require.NoError(t, err)

	// Create a webhook receiver
	webhookReceiver := NewWebhookReceiver()
	t.Cleanup(webhookReceiver.Close)

	// Step 1: Create a webhook
	webhookPayload := webhook.RegisterWebhookRequest{
		Service:           triggersrepo.ServiceSignal,
		MetricName:        "speed",
		Condition:         "valueNumber > 20 && valueNumber != previousValue",
		CoolDownPeriod:    0,
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
		fmt.Sprintf(`{"query":"\n\tquery($clientId: Address){\n\t\tdeveloperLicense(by: { clientId: $clientId }) {\n\t\t\tclientId\n\t\t}\n\t}","variables":{"clientId":"%s"}}`, devAddress.String()),
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

	resp, err := servers.Application.Test(req, -1)
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

	// Step 2: Subscribe a vehicle to the webhook

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

	resp, err = servers.Application.Test(req, -1)
	require.NoError(t, err)
	require.Equal(t, http.StatusCreated, resp.StatusCode)

	// wait for webhook to be updated
	time.Sleep(1 * time.Second)

	// Step 3: Send a signal to Kafka to trigger the webhook
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

	err = tc.Kafka.PushJSONToTopic(settingsCopy.DeviceSignalsTopic, signalPayload)
	require.NoError(t, err)

	// Step 4: Verify the webhook was called

	// Wait for the webhook to be called (with timeout)
	received := webhookReceiver.WaitForCall(10 * time.Second)
	require.True(t, received, "Webhook was not called within timeout")

	// Get the received calls
	calls := webhookReceiver.GetReceivedCalls()
	require.Len(t, calls, 1, "Expected exactly one webhook call")

	call := calls[0]

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
	webhookReceiver.ClearReceivedCalls()

	// Step 5: Send a second signal to Kafka to trigger the webhook
	signalPayload = vss.Signal{
		TokenID:      12345, // Same as assetDID.TokenID
		Timestamp:    time.Now(),
		Name:         "speed",
		ValueNumber:  25.0, // Above the 20 threshold to trigger the webhook
		ValueString:  "",
		Source:       "test-source",
		Producer:     "test-producer",
		CloudEventID: "test-event-id",
	}
	if err := tc.Kafka.PushJSONToTopic(settingsCopy.DeviceSignalsTopic, signalPayload); err != nil {
		t.Errorf("failed to push signal to Kafka: %v", err)
	}

	// Step 6: Verify the webhook was not called
	received = webhookReceiver.WaitForCall(2 * time.Second)
	require.False(t, received, "Webhook was unexpectedly called within timeout")
	calls = webhookReceiver.GetReceivedCalls()
	require.Len(t, calls, 0, "Expected exactly one webhook call")
	signalPayload = vss.Signal{
		TokenID:      12345, // Same as assetDID.TokenID
		Timestamp:    time.Now(),
		Name:         "speed",
		ValueNumber:  24.0, // Below the 20 threshold to trigger the webhook
		ValueString:  "",
		Source:       "test-source",
		Producer:     "test-producer",
		CloudEventID: "test-event-id",
	}
	if err := tc.Kafka.PushJSONToTopic(settingsCopy.DeviceSignalsTopic, signalPayload); err != nil {
		t.Errorf("failed to push signal to Kafka: %v", err)
	}

	// Step 7: Verify the webhook was called
	received = webhookReceiver.WaitForCall(10 * time.Second)
	require.True(t, received, "Webhook was not called within timeout")
	calls = webhookReceiver.GetReceivedCalls()
	require.Len(t, calls, 1, "Expected exactly one webhook call")

	call = calls[0]

	// Verify the webhook call details
	require.Equal(t, "POST", call.Method)
	require.Equal(t, "/", call.URL) // The webhook receiver URL path

	// Parse the webhook body to verify it contains the signal data
	webhookBodyData = make(map[string]any)
	err = json.Unmarshal([]byte(call.Body), &webhookBodyData)
	require.NoError(t, err)

	// Verify the webhook contains the expected signal data
	require.Contains(t, webhookBodyData, "data")
	data = webhookBodyData["data"].(map[string]any)
	require.Contains(t, data, "signal")
	signal = data["signal"].(map[string]any)
	require.Equal(t, float64(24), signal["value"].(float64))
	require.Equal(t, "speed", signal["name"].(string))
}

func TestSignalWebhookFlowLocation(t *testing.T) {
	t.Parallel()
	tc := GetTestServices(t)

	// Create a developer address for testing
	devAddress := tests.RandomAddr(t)
	settingsCopy := tc.Settings
	settingsCopy.DeviceEventsTopic = "test-event-topic" + devAddress.String()
	settingsCopy.DeviceSignalsTopic = "test-signal-topic" + devAddress.String()

	// Create the main application
	servers, err := app.CreateServers(t.Context(), &settingsCopy, zerolog.New(os.Stdout))
	go func() {
		if err := servers.SignalConsumer.Start(t.Context()); err != nil {
			if !errors.Is(err, context.Canceled) {
				t.Errorf("failed to start signal consumer: %v", err)
			}
		}
	}()
	go func() {
		if err := servers.EventConsumer.Start(t.Context()); err != nil {
			if !errors.Is(err, context.Canceled) {
				t.Errorf("failed to start event consumer: %v", err)
			}
		}
	}()
	t.Cleanup(func() {
		_ = servers.SignalConsumer.Stop(t.Context())
		_ = servers.EventConsumer.Stop(t.Context())
	})
	require.NoError(t, err)

	// Create a webhook receiver
	webhookReceiver := NewWebhookReceiver()
	t.Cleanup(webhookReceiver.Close)

	// Step 1: Create a webhook for location coordinates
	webhookPayload := webhook.RegisterWebhookRequest{
		Service:           triggersrepo.ServiceSignal,
		MetricName:        "currentLocationCoordinates",
		Condition:         "geoDistance(value.Latitude, value.Longitude, 54.71061320000001, 25.239925999999997) < 0.7138406571965812",
		CoolDownPeriod:    0,
		Description:       "Alert when vehicle is within 0.7km of target location",
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
		fmt.Sprintf(`{"query":"\n\tquery($clientId: Address){\n\t\tdeveloperLicense(by: { clientId: $clientId }) {\n\t\t\tclientId\n\t\t}\n\t}","variables":{"clientId":"%s"}}`, devAddress.String()),
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

	resp, err := servers.Application.Test(req, -1)
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

	// Step 2: Subscribe a vehicle to the webhook

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

	resp, err = servers.Application.Test(req, -1)
	require.NoError(t, err)
	require.Equal(t, http.StatusCreated, resp.StatusCode)

	// wait for webhook to be updated
	time.Sleep(1 * time.Second)

	// Step 3: Send a location signal to Kafka to trigger the webhook
	// Using coordinates that are within the 0.7km radius of the target location (54.71061320000001, 25.239925999999997)
	signalPayload := vss.Signal{
		TokenID:     12345, // Same as assetDID.TokenID
		Timestamp:   time.Now(),
		Name:        "currentLocationCoordinates",
		ValueNumber: 0,
		ValueString: "",
		ValueLocation: vss.Location{
			Latitude:  54.71061320000001,  // Same as target latitude
			Longitude: 25.239925999999997, // Same as target longitude
			HDOP:      1.0,                // Horizontal Dilution of Precision
		},
		Source:       "test-source",
		Producer:     "test-producer",
		CloudEventID: "test-event-id",
	}

	err = tc.Kafka.PushJSONToTopic(settingsCopy.DeviceSignalsTopic, signalPayload)
	require.NoError(t, err)

	// Step 4: Verify the webhook was called

	// Wait for the webhook to be called (with timeout)
	received := webhookReceiver.WaitForCall(10 * time.Second)
	require.True(t, received, "Webhook was not called within timeout")

	// Get the received calls
	calls := webhookReceiver.GetReceivedCalls()
	require.Len(t, calls, 1, "Expected exactly one webhook call")

	call := calls[0]

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
	require.Equal(t, "currentLocationCoordinates", signal["name"].(string))
	require.Equal(t, "vss.Location", signal["valueType"].(string))
	// Verify the location value is a map with latitude, longitude, and HDOP
	locationValue := signal["value"].(map[string]any)
	require.Equal(t, 54.71061320000001, locationValue["Latitude"].(float64))
	require.Equal(t, 25.239925999999997, locationValue["Longitude"].(float64))
	require.Equal(t, 1.0, locationValue["HDOP"].(float64))
	webhookReceiver.ClearReceivedCalls()

	// Step 5: Send a location signal with coordinates outside the radius
	signalPayload = vss.Signal{
		TokenID:     12345, // Same as assetDID.TokenID
		Timestamp:   time.Now(),
		Name:        "currentLocationCoordinates",
		ValueNumber: 0,
		ValueString: "",
		ValueLocation: vss.Location{
			Latitude:  55.0, // Far from target location (outside 0.7km radius)
			Longitude: 26.0,
			HDOP:      1.0,
		},
		Source:       "test-source",
		Producer:     "test-producer",
		CloudEventID: "test-event-id",
	}
	if err := tc.Kafka.PushJSONToTopic(settingsCopy.DeviceSignalsTopic, signalPayload); err != nil {
		t.Errorf("failed to push signal to Kafka: %v", err)
	}

	// Step 6: Verify the webhook was not called (coordinates outside radius)
	received = webhookReceiver.WaitForCall(2 * time.Second)
	require.False(t, received, "Webhook was unexpectedly called for coordinates outside radius")
	calls = webhookReceiver.GetReceivedCalls()
	require.Len(t, calls, 0, "Expected no webhook calls for coordinates outside radius")
}
func TestEventWebhookFlow(t *testing.T) {
	t.Parallel()
	tc := GetTestServices(t)

	// Create a developer address for testing
	devAddress := tests.RandomAddr(t)
	settingsCopy := tc.Settings
	settingsCopy.DeviceEventsTopic = "test-event-topic" + devAddress.String()
	settingsCopy.DeviceSignalsTopic = "test-signal-topic" + devAddress.String()

	// Create the main application
	servers, err := app.CreateServers(t.Context(), &settingsCopy, zerolog.New(os.Stdout))
	go func() {
		if err := servers.SignalConsumer.Start(t.Context()); err != nil {
			if !errors.Is(err, context.Canceled) {
				t.Errorf("failed to start signal consumer: %v", err)
			}
		}
	}()
	go func() {
		if err := servers.EventConsumer.Start(t.Context()); err != nil {
			if !errors.Is(err, context.Canceled) {
				t.Errorf("failed to start event consumer: %v", err)
			}
		}
	}()
	t.Cleanup(func() {
		_ = servers.SignalConsumer.Stop(t.Context())
		_ = servers.EventConsumer.Stop(t.Context())
	})
	require.NoError(t, err)

	// Create a webhook receiver
	webhookReceiver := NewWebhookReceiver()
	t.Cleanup(webhookReceiver.Close)

	// Step 1: Create a webhook
	webhookPayload := webhook.RegisterWebhookRequest{
		Service:           triggersrepo.ServiceEvent,
		MetricName:        "HarshBraking",
		Condition:         "true",
		CoolDownPeriod:    0,
		Description:       "Alert when vehicle harsh braking occurs",
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
		fmt.Sprintf(`{"query":"\n\tquery($clientId: Address){\n\t\tdeveloperLicense(by: { clientId: $clientId }) {\n\t\t\tclientId\n\t\t}\n\t}","variables":{"clientId":"%s"}}`, devAddress.String()),
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

	resp, err := servers.Application.Test(req, -1)
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

	// Step 2: Subscribe a vehicle to the webhook

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

	resp, err = servers.Application.Test(req, -1)
	require.NoError(t, err)
	require.Equal(t, http.StatusCreated, resp.StatusCode)

	// wait for webhook to be updated
	time.Sleep(1 * time.Second)

	// Step 3: Send a signal to Kafka to trigger the webhook
	eventPayload := []vss.Event{
		{
			Subject:      assetDid.String(),
			Timestamp:    time.Now(),
			Name:         "HarshBraking",
			Source:       "test-source",
			Producer:     "test-producer",
			CloudEventID: "test-event-id",
			DurationNs:   1000000000,
			Metadata:     `{"counter": 1}`,
		},
	}

	err = tc.Kafka.PushJSONToTopic(settingsCopy.DeviceEventsTopic, eventPayload)
	require.NoError(t, err)

	// Step 4: Verify the webhook was called
	// Wait for the webhook to be called (with timeout)
	received := webhookReceiver.WaitForCall(10 * time.Second)
	require.True(t, received, "Webhook was not called within timeout")

	// Get the received calls
	calls := webhookReceiver.GetReceivedCalls()
	require.Len(t, calls, 1, "Expected exactly one webhook call")

	call := calls[0]

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
	require.Contains(t, data, "event")
	event := data["event"].(map[string]any)
	require.Equal(t, "HarshBraking", event["name"].(string))
	require.Equal(t, "test-source", event["source"].(string))
	require.Equal(t, "test-producer", event["producer"].(string))
	require.Equal(t, float64(1000000000), event["durationNs"].(float64))
	require.Equal(t, `{"counter": 1}`, event["metadata"].(string))

}
