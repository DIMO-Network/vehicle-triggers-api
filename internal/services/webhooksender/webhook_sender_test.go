package webhooksender

import (
	"context"
	"crypto/tls"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/DIMO-Network/cloudevent"
	"github.com/DIMO-Network/server-garage/pkg/richerrors"
	"github.com/DIMO-Network/vehicle-triggers-api/internal/controllers/webhook"
	"github.com/DIMO-Network/vehicle-triggers-api/internal/db/models"
	"github.com/ethereum/go-ethereum/common"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestWebhookSender_SendWebhook(t *testing.T) {
	t.Parallel()

	t.Run("successful webhook delivery", func(t *testing.T) {
		// Setup test server
		testServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			// Verify request headers
			assert.Equal(t, "application/json", r.Header.Get("Content-Type"))
			assert.Equal(t, "DIMO-Webhook/1.0", r.Header.Get("User-Agent"))
			assert.Equal(t, http.MethodPost, r.Method)

			// Verify payload can be parsed
			body, err := io.ReadAll(r.Body)
			require.NoError(t, err)

			var payload cloudevent.CloudEvent[webhook.WebhookPayload]
			err = json.Unmarshal(body, &payload)
			require.NoError(t, err)
			assert.Equal(t, "test-webhook-id", payload.Data.WebhookId)

			w.WriteHeader(http.StatusOK)
			_, _ = fmt.Fprint(w, "success")
		}))
		defer testServer.Close()

		sender := NewWebhookSender(nil)
		trigger := &models.Trigger{
			ID:                      "test-webhook-id",
			TargetURI:               testServer.URL,
			DeveloperLicenseAddress: common.HexToAddress("0x1234").Bytes(),
		}

		payload := createTestPayload("test-webhook-id")
		ctx := context.Background()

		err := sender.SendWebhook(ctx, trigger, payload)
		assert.NoError(t, err)
	})

	t.Run("webhook returns 400 error", func(t *testing.T) {
		testServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			w.WriteHeader(http.StatusBadRequest)
			_, _ = fmt.Fprint(w, "invalid request")
		}))
		defer testServer.Close()

		sender := NewWebhookSender(nil)
		trigger := &models.Trigger{
			ID:        "test-webhook-id",
			TargetURI: testServer.URL,
		}

		payload := createTestPayload("test-webhook-id")
		ctx := context.Background()

		err := sender.SendWebhook(ctx, trigger, payload)
		require.Error(t, err)

		// Should be a webhook failure error
		richErr, ok := richerrors.AsRichError(err)
		require.True(t, ok)
		assert.Equal(t, WebhookFailureCode, richErr.Code)
	})

	t.Run("webhook returns 500 error", func(t *testing.T) {
		testServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			w.WriteHeader(http.StatusInternalServerError)
			_, _ = fmt.Fprint(w, "server error")
		}))
		defer testServer.Close()

		sender := NewWebhookSender(nil)
		trigger := &models.Trigger{
			ID:        "test-webhook-id",
			TargetURI: testServer.URL,
		}

		payload := createTestPayload("test-webhook-id")
		ctx := context.Background()

		err := sender.SendWebhook(ctx, trigger, payload)
		require.Error(t, err)

		richErr, ok := richerrors.AsRichError(err)
		require.True(t, ok)
		assert.Equal(t, WebhookFailureCode, richErr.Code)
	})

	t.Run("network connection failure", func(t *testing.T) {
		sender := NewWebhookSender(nil)
		trigger := &models.Trigger{
			ID:        "test-webhook-id",
			TargetURI: "http://invalid.localhost:0", // Invalid endpoint
		}

		payload := createTestPayload("test-webhook-id")
		ctx := context.Background()

		err := sender.SendWebhook(ctx, trigger, payload)
		require.Error(t, err)

		richErr, ok := richerrors.AsRichError(err)
		require.True(t, ok)
		assert.Equal(t, WebhookFailureCode, richErr.Code)
	})

	t.Run("invalid URL format", func(t *testing.T) {
		sender := NewWebhookSender(nil)
		trigger := &models.Trigger{
			ID:        "test-webhook-id",
			TargetURI: "://invalid-url", // Invalid URL format
		}

		payload := createTestPayload("test-webhook-id")
		ctx := context.Background()

		err := sender.SendWebhook(ctx, trigger, payload)
		require.Error(t, err)
		richErr, ok := richerrors.AsRichError(err)
		require.True(t, ok)
		assert.Equal(t, WebhookFailureCode, richErr.Code)
	})

	t.Run("request timeout", func(t *testing.T) {
		// Setup server that delays response longer than client timeout
		testServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			time.Sleep(100 * time.Millisecond) // Delay longer than client timeout
			w.WriteHeader(http.StatusOK)
		}))
		defer testServer.Close()

		// Create client with very short timeout
		client := &http.Client{
			Timeout: 10 * time.Millisecond,
		}
		sender := NewWebhookSender(client)

		trigger := &models.Trigger{
			ID:        "test-webhook-id",
			TargetURI: testServer.URL,
		}

		payload := createTestPayload("test-webhook-id")
		ctx := context.Background()

		err := sender.SendWebhook(ctx, trigger, payload)
		require.Error(t, err)

		richErr, ok := richerrors.AsRichError(err)
		require.True(t, ok)
		assert.Equal(t, WebhookFailureCode, richErr.Code)
	})

	t.Run("context cancellation", func(t *testing.T) {
		testServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			time.Sleep(100 * time.Millisecond) // Delay to allow context cancellation
			w.WriteHeader(http.StatusOK)
		}))
		defer testServer.Close()

		sender := NewWebhookSender(nil)
		trigger := &models.Trigger{
			ID:        "test-webhook-id",
			TargetURI: testServer.URL,
		}

		payload := createTestPayload("test-webhook-id")
		ctx, cancel := context.WithCancel(context.Background())

		// Cancel context after a short delay
		go func() {
			time.Sleep(10 * time.Millisecond)
			cancel()
		}()

		err := sender.SendWebhook(ctx, trigger, payload)
		require.Error(t, err)

		richErr, ok := richerrors.AsRichError(err)
		require.True(t, ok)
		assert.Equal(t, WebhookFailureCode, richErr.Code)
	})

	t.Run("large response body is truncated", func(t *testing.T) {
		largeResponse := strings.Repeat("x", 2048) // Larger than maxResponseBodySize
		testServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			w.WriteHeader(http.StatusBadRequest)
			_, _ = fmt.Fprint(w, largeResponse)
		}))
		defer testServer.Close()

		sender := NewWebhookSender(nil)
		trigger := &models.Trigger{
			ID:        "test-webhook-id",
			TargetURI: testServer.URL,
		}

		payload := createTestPayload("test-webhook-id")
		ctx := context.Background()

		err := sender.SendWebhook(ctx, trigger, payload)
		require.Error(t, err)

		// Error message should contain truncated response (limited by maxResponseBodySize)
		assert.Contains(t, err.Error(), "webhook returned status code 400")
		// The full large response should not be in the error (should be truncated)
		assert.True(t, len(err.Error()) <= maxResponseBodySize+50, "Response should be truncated") // +50 for buffer
	})

	t.Run("HTTPS server with custom client", func(t *testing.T) {
		testServer := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			w.WriteHeader(http.StatusOK)
			_, _ = fmt.Fprint(w, "success")
		}))
		defer testServer.Close()

		// Create client that accepts self-signed certificates
		client := &http.Client{
			Transport: &http.Transport{
				TLSClientConfig: &tls.Config{InsecureSkipVerify: true}, //nolint:gosec // test only
			},
		}

		sender := NewWebhookSender(client)
		trigger := &models.Trigger{
			ID:        "test-webhook-id",
			TargetURI: testServer.URL,
		}

		payload := createTestPayload("test-webhook-id")
		ctx := context.Background()

		err := sender.SendWebhook(ctx, trigger, payload)
		assert.NoError(t, err)
	})

	t.Run("successful 201 response", func(t *testing.T) {
		testServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			w.WriteHeader(http.StatusCreated) // 201 is still success
			_, _ = fmt.Fprint(w, "created")
		}))
		defer testServer.Close()

		sender := NewWebhookSender(nil)
		trigger := &models.Trigger{
			ID:        "test-webhook-id",
			TargetURI: testServer.URL,
		}

		payload := createTestPayload("test-webhook-id")
		ctx := context.Background()

		err := sender.SendWebhook(ctx, trigger, payload)
		assert.NoError(t, err)
	})

	t.Run("empty response body on error", func(t *testing.T) {
		testServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			w.WriteHeader(http.StatusNotFound)
			// No response body
		}))
		defer testServer.Close()

		sender := NewWebhookSender(nil)
		trigger := &models.Trigger{
			ID:        "test-webhook-id",
			TargetURI: testServer.URL,
		}

		payload := createTestPayload("test-webhook-id")
		ctx := context.Background()

		err := sender.SendWebhook(ctx, trigger, payload)
		require.Error(t, err)

		richErr, ok := richerrors.AsRichError(err)
		require.True(t, ok)
		assert.Equal(t, WebhookFailureCode, richErr.Code)
	})
}

func TestNewWebhookSender(t *testing.T) {
	t.Parallel()

	t.Run("with nil client creates default", func(t *testing.T) {
		sender := NewWebhookSender(nil)
		require.NotNil(t, sender)
		require.NotNil(t, sender.client)
		assert.Equal(t, defaultWebhookTimeout, sender.client.Timeout)
	})

	t.Run("with custom client uses provided", func(t *testing.T) {
		customTimeout := 5 * time.Second
		customClient := &http.Client{
			Timeout: customTimeout,
		}

		sender := NewWebhookSender(customClient)
		require.NotNil(t, sender)
		assert.Equal(t, customClient, sender.client)
		assert.Equal(t, customTimeout, sender.client.Timeout)
	})
}

// Helper function to create test webhook payload
func createTestPayload(webhookID string) *cloudevent.CloudEvent[webhook.WebhookPayload] {
	assetDID := cloudevent.ERC721DID{
		ChainID:         137,
		ContractAddress: common.HexToAddress("0xbA5738a18d83D41847dfFbDC6101d37C69c9B0cF"),
		TokenID:         common.Big1,
	}

	return &cloudevent.CloudEvent[webhook.WebhookPayload]{
		CloudEventHeader: cloudevent.CloudEventHeader{
			ID:              "test-event-id",
			Source:          "vehicle-triggers-api",
			Subject:         assetDID.String(),
			Time:            time.Now().UTC(),
			DataContentType: "application/json",
			DataVersion:     "telemetry.signals/v1.0",
			Type:            "dimo.trigger",
			SpecVersion:     "1.0",
		},
		Data: webhook.WebhookPayload{
			Service:     "telemetry.signals",
			MetricName:  "speed",
			WebhookId:   webhookID,
			WebhookName: "Test Webhook",
			AssetDID:    assetDID,
			Condition:   "valueNumber > 55",
		},
	}
}
