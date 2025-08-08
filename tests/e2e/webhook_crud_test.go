package e2e_test

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"testing"

	"github.com/DIMO-Network/vehicle-triggers-api/internal/app"
	"github.com/DIMO-Network/vehicle-triggers-api/internal/controllers/webhook"
	"github.com/DIMO-Network/vehicle-triggers-api/internal/services/triggersrepo"
	"github.com/ethereum/go-ethereum/common"
	"github.com/rs/zerolog"
	"github.com/stretchr/testify/require"
)

func TestWebhookCRUDOperations(t *testing.T) {
	t.Parallel()
	tc := GetTestServices(t)

	// Create the main application
	fiberApp, err := app.CreateServers(t.Context(), &tc.Settings, zerolog.New(os.Stdout))
	require.NoError(t, err)

	// Create a webhook receiver
	webhookReceiver := NewWebhookReceiver()
	defer webhookReceiver.Close()

	// Create a developer address for testing
	devAddress := common.HexToAddress("0xde02")

	// Create auth token for the request
	authToken, err := tc.Auth.CreateToken(t, devAddress)
	require.NoError(t, err)

	// Set up identity API mock to return developer license
	err = tc.Identity.SetRequestResponse(
		fmt.Sprintf(`{"query":"\n\t\tquery($clientId: Address){\n\t\tdeveloperLicense(by: { clientId: $clientId }) {\n\t\t\tclientId\n\t\t\ttokenId\n\t\t}\n\t}","variables":{"clientId":"%s"}}`, devAddress.String()),
		map[string]any{
			"data": map[string]any{
				"developerLicense": map[string]any{
					"clientId": devAddress.String(),
					"tokenId":  54321,
				},
			},
		})
	require.NoError(t, err)

	// Set up token exchange API mock to return permissions for vehicles
	tc.TokenExchange.SetAccessCheckReturn(devAddress.String(), true)

	// Test vehicle token IDs
	vehicleTokenID1 := "12345"
	vehicleTokenID2 := "67890"
	var webhookID string
	t.Run("Step 1: Create Webhook", func(t *testing.T) {
		t.Log("Creating initial webhook")

		webhookPayload := webhook.RegisterWebhookRequest{
			Service:           "Telemetry",
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
		var ok bool
		webhookID, ok = webhookResponse["id"].(string)
		require.True(t, ok, "Expected webhook ID in response")
		require.NotEmpty(t, webhookID)

	})
	require.NotEmpty(t, webhookID, "Webhook ID should be set from previous test")

	t.Run("Step 2: List Webhooks", func(t *testing.T) {
		t.Log("Listing webhooks")

		req, err := http.NewRequestWithContext(
			t.Context(),
			"GET",
			"/v1/webhooks",
			nil,
		)
		require.NoError(t, err)

		req.Header.Set("Authorization", "Bearer "+authToken)

		resp, err := fiberApp.Test(req, -1)
		require.NoError(t, err)
		require.Equal(t, http.StatusOK, resp.StatusCode)

		body, err := io.ReadAll(resp.Body)
		require.NoError(t, err)

		var webhooks []map[string]any
		err = json.Unmarshal(body, &webhooks)
		require.NoError(t, err)
		require.Len(t, webhooks, 1, "Should have exactly one webhook")
		require.Equal(t, webhookID, webhooks[0]["id"])
		t.Logf("Found %d webhooks", len(webhooks))
	})

	t.Run("Step 3: Assign Vehicle to Webhook", func(t *testing.T) {
		t.Log("Assigning vehicle to webhook")

		subscribeURL := fmt.Sprintf("/v1/webhooks/%s/subscribe/%s", webhookID, vehicleTokenID1)
		req, err := http.NewRequestWithContext(
			t.Context(),
			"POST",
			subscribeURL,
			nil,
		)
		require.NoError(t, err)

		req.Header.Set("Authorization", "Bearer "+authToken)

		resp, err := fiberApp.Test(req, -1)
		require.NoError(t, err)
		require.Equal(t, http.StatusCreated, resp.StatusCode)

		t.Logf("Assigned vehicle %s to webhook %s", vehicleTokenID1, webhookID)
	})

	t.Run("Step 4: List Vehicles for Webhook", func(t *testing.T) {
		t.Log("Listing vehicles subscribed to webhook")

		req, err := http.NewRequestWithContext(
			t.Context(),
			"GET",
			fmt.Sprintf("/v1/webhooks/%s", webhookID),
			nil,
		)
		require.NoError(t, err)

		req.Header.Set("Authorization", "Bearer "+authToken)

		resp, err := fiberApp.Test(req, -1)
		require.NoError(t, err)
		require.Equal(t, http.StatusOK, resp.StatusCode)

		body, err := io.ReadAll(resp.Body)
		require.NoError(t, err)

		var vehicleIDs []string
		err = json.Unmarshal(body, &vehicleIDs)
		require.NoError(t, err)
		require.Len(t, vehicleIDs, 1, "Should have exactly one vehicle subscribed")
		require.Equal(t, vehicleTokenID1, vehicleIDs[0])
		t.Logf("Found %d vehicles subscribed to webhook", len(vehicleIDs))
	})

	t.Run("Step 5: Assign Second Vehicle to Webhook", func(t *testing.T) {
		t.Log("Assigning second vehicle to webhook")

		subscribeURL := fmt.Sprintf("/v1/webhooks/%s/subscribe/%s", webhookID, vehicleTokenID2)
		req, err := http.NewRequestWithContext(
			t.Context(),
			"POST",
			subscribeURL,
			nil,
		)
		require.NoError(t, err)

		req.Header.Set("Authorization", "Bearer "+authToken)

		resp, err := fiberApp.Test(req, -1)
		require.NoError(t, err)
		require.Equal(t, http.StatusCreated, resp.StatusCode)

		t.Logf("Assigned vehicle %s to webhook %s", vehicleTokenID2, webhookID)
	})

	t.Run("Step 6: Verify Both Vehicles are Subscribed", func(t *testing.T) {
		t.Log("Verifying both vehicles are subscribed")

		req, err := http.NewRequestWithContext(
			t.Context(),
			"GET",
			fmt.Sprintf("/v1/webhooks/%s", webhookID),
			nil,
		)
		require.NoError(t, err)

		req.Header.Set("Authorization", "Bearer "+authToken)

		resp, err := fiberApp.Test(req, -1)
		require.NoError(t, err)
		require.Equal(t, http.StatusOK, resp.StatusCode)

		body, err := io.ReadAll(resp.Body)
		require.NoError(t, err)

		var vehicleIDs []string
		err = json.Unmarshal(body, &vehicleIDs)
		require.NoError(t, err)
		require.Len(t, vehicleIDs, 2, "Should have exactly two vehicles subscribed")

		// Check that both vehicle IDs are present
		vehicleMap := make(map[string]bool)
		for _, id := range vehicleIDs {
			vehicleMap[id] = true
		}
		require.True(t, vehicleMap[vehicleTokenID1], "First vehicle should be subscribed")
		require.True(t, vehicleMap[vehicleTokenID2], "Second vehicle should be subscribed")
		t.Logf("Verified %d vehicles are subscribed to webhook", len(vehicleIDs))
	})

	t.Run("Step 7: Update Webhook", func(t *testing.T) {
		t.Log("Updating webhook configuration")

		updatePayload := webhook.UpdateWebhookRequest{
			Description:    ref("Updated description for speed alert"),
			CoolDownPeriod: ref(15),
			Status:         ref(triggersrepo.StatusEnabled),
		}

		updateBody, err := json.Marshal(updatePayload)
		require.NoError(t, err)

		req, err := http.NewRequestWithContext(
			t.Context(),
			"PUT",
			fmt.Sprintf("/v1/webhooks/%s", webhookID),
			bytes.NewBuffer(updateBody),
		)
		require.NoError(t, err)

		req.Header.Set("Content-Type", "application/json")
		req.Header.Set("Authorization", "Bearer "+authToken)

		resp, err := fiberApp.Test(req, -1)
		require.NoError(t, err)
		require.Equal(t, http.StatusOK, resp.StatusCode)

		body, err := io.ReadAll(resp.Body)
		require.NoError(t, err)

		var response map[string]any
		err = json.Unmarshal(body, &response)
		require.NoError(t, err)
		require.Equal(t, webhookID, response["id"])
		require.Equal(t, "Webhook updated successfully", response["message"])

		t.Logf("Updated webhook %s", webhookID)
	})

	t.Run("Step 8: Unassign First Vehicle from Webhook", func(t *testing.T) {
		t.Log("Unassigning first vehicle from webhook")

		unsubscribeURL := fmt.Sprintf("/v1/webhooks/%s/unsubscribe/%s", webhookID, vehicleTokenID1)
		req, err := http.NewRequestWithContext(
			t.Context(),
			"DELETE",
			unsubscribeURL,
			nil,
		)
		require.NoError(t, err)

		req.Header.Set("Authorization", "Bearer "+authToken)

		resp, err := fiberApp.Test(req, -1)
		require.NoError(t, err)
		require.Equal(t, http.StatusOK, resp.StatusCode)

		t.Logf("Unassigned vehicle %s from webhook %s", vehicleTokenID1, webhookID)
	})

	t.Run("Step 9: Verify Only Second Vehicle is Subscribed", func(t *testing.T) {
		t.Log("Verifying only second vehicle is subscribed")

		req, err := http.NewRequestWithContext(
			t.Context(),
			"GET",
			fmt.Sprintf("/v1/webhooks/%s", webhookID),
			nil,
		)
		require.NoError(t, err)

		req.Header.Set("Authorization", "Bearer "+authToken)

		resp, err := fiberApp.Test(req, -1)
		require.NoError(t, err)
		require.Equal(t, http.StatusOK, resp.StatusCode)

		body, err := io.ReadAll(resp.Body)
		require.NoError(t, err)

		var vehicleIDs []string
		err = json.Unmarshal(body, &vehicleIDs)
		require.NoError(t, err)
		require.Len(t, vehicleIDs, 1, "Should have exactly one vehicle subscribed")
		require.Equal(t, vehicleTokenID2, vehicleIDs[0])
		t.Logf("Verified only vehicle %s is subscribed to webhook", vehicleTokenID2)
	})

	t.Run("Step 10: Unassign All Vehicles from Webhook", func(t *testing.T) {
		t.Log("Unassigning all vehicles from webhook")

		unsubscribeAllURL := fmt.Sprintf("/v1/webhooks/%s/unsubscribe/all", webhookID)
		req, err := http.NewRequestWithContext(
			t.Context(),
			"DELETE",
			unsubscribeAllURL,
			nil,
		)
		require.NoError(t, err)

		req.Header.Set("Authorization", "Bearer "+authToken)

		resp, err := fiberApp.Test(req, -1)
		require.NoError(t, err)
		require.Equal(t, http.StatusOK, resp.StatusCode)

		body, err := io.ReadAll(resp.Body)
		require.NoError(t, err)

		var response map[string]any
		err = json.Unmarshal(body, &response)
		require.NoError(t, err)
		require.Contains(t, response["message"], "Unsubscribed")

		t.Logf("Unassigned all vehicles from webhook %s", webhookID)
	})

	t.Run("Step 11: Verify No Vehicles are Subscribed", func(t *testing.T) {
		t.Log("Verifying no vehicles are subscribed")

		req, err := http.NewRequestWithContext(
			t.Context(),
			"GET",
			fmt.Sprintf("/v1/webhooks/%s", webhookID),
			nil,
		)
		require.NoError(t, err)

		req.Header.Set("Authorization", "Bearer "+authToken)

		resp, err := fiberApp.Test(req, -1)
		require.NoError(t, err)
		require.Equal(t, http.StatusOK, resp.StatusCode)

		body, err := io.ReadAll(resp.Body)
		require.NoError(t, err)

		var vehicleIDs []string
		err = json.Unmarshal(body, &vehicleIDs)
		require.NoError(t, err)
		require.Len(t, vehicleIDs, 0, "Should have no vehicles subscribed")
		t.Logf("Verified no vehicles are subscribed to webhook")
	})

	t.Run("Step 12: Delete Webhook", func(t *testing.T) {
		t.Log("Deleting webhook")

		req, err := http.NewRequestWithContext(
			t.Context(),
			"DELETE",
			fmt.Sprintf("/v1/webhooks/%s", webhookID),
			nil,
		)
		require.NoError(t, err)

		req.Header.Set("Authorization", "Bearer "+authToken)

		resp, err := fiberApp.Test(req, -1)
		require.NoError(t, err)
		require.Equal(t, http.StatusOK, resp.StatusCode)

		body, err := io.ReadAll(resp.Body)
		require.NoError(t, err)

		var response map[string]any
		err = json.Unmarshal(body, &response)
		require.NoError(t, err)
		require.Equal(t, "Webhook deleted successfully", response["message"])

		t.Logf("Deleted webhook %s", webhookID)
	})

	t.Run("Step 13: Verify Webhook is Deleted", func(t *testing.T) {
		t.Log("Verifying webhook is deleted")

		req, err := http.NewRequestWithContext(
			t.Context(),
			"GET",
			"/v1/webhooks",
			nil,
		)
		require.NoError(t, err)

		req.Header.Set("Authorization", "Bearer "+authToken)

		resp, err := fiberApp.Test(req, -1)
		require.NoError(t, err)
		require.Equal(t, http.StatusOK, resp.StatusCode)

		body, err := io.ReadAll(resp.Body)
		require.NoError(t, err)

		var webhooks []map[string]any
		err = json.Unmarshal(body, &webhooks)
		require.NoError(t, err)
		require.Len(t, webhooks, 0, "Should have no webhooks after deletion")
		t.Logf("Verified webhook is deleted - found %d webhooks", len(webhooks))
	})
}

func ref[T any](v T) *T {
	return &v
}
