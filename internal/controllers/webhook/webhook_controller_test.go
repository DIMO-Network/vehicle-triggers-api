//go:generate go tool mockgen -source=webhook_controller.go -destination=webhook_controller_mock_test.go -package=webhook
package webhook

import (
	"bytes"
	"context"
	"crypto/tls"
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"testing"

	"github.com/DIMO-Network/model-garage/pkg/vss"
	"github.com/DIMO-Network/server-garage/pkg/fibercommon"
	"github.com/DIMO-Network/vehicle-triggers-api/internal/auth"
	"github.com/DIMO-Network/vehicle-triggers-api/internal/db/models"
	"github.com/ethereum/go-ethereum/common"
	"github.com/gofiber/fiber/v2"
	"github.com/golang-jwt/jwt/v5"
	"github.com/google/uuid"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"github.com/volatiletech/null/v8"
	"go.uber.org/mock/gomock"
)

func TestMain(m *testing.M) {
	// Ensure the application's HTTP client (which uses the default transport)
	// accepts the self-signed certificate used by the TLS test server.
	if transport, ok := http.DefaultTransport.(*http.Transport); ok {
		// Clone or initialize TLS config and skip verification for tests.
		tlsCfg := transport.TLSClientConfig
		if tlsCfg == nil {
			tlsCfg = &tls.Config{}
		}
		tlsCfg.InsecureSkipVerify = true //nolint:gosec // test-only: allow self-signed cert from httptest server
		transport.TLSClientConfig = tlsCfg
	}

	flag.Parse()
	os.Exit(m.Run())
}

func TestWebhookController_RegisterWebhook(t *testing.T) {
	t.Parallel()

	t.Run("successful webhook registration", func(t *testing.T) {
		controller, mockRepo, _ := newWebhookControllerAndMocks(t)

		app := newApp()
		devLicense := common.HexToAddress("0x1234567890abcdef")
		app.Use(tokenInjector(devLicense))
		app.Post("/webhooks", controller.RegisterWebhook)

		// Setup test server for webhook verification
		testServer := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			w.WriteHeader(http.StatusOK)
			fmt.Fprint(w, "test-token")
		}))
		defer testServer.Close()

		payload := RegisterWebhookRequest{
			Service:           "telemetry.signals",
			MetricName:        "speed",
			Condition:         "valueNumber > 55",
			CoolDownPeriod:    30,
			Description:       "Speed alert webhook",
			DisplayName:       "Speed Alert",
			TargetURL:         testServer.URL,
			Status:            "enabled",
			VerificationToken: "test-token",
		}

		expectedTrigger := &models.Trigger{
			ID:             "test-trigger-id",
			Service:        "telemetry.signals",
			MetricName:     "speed",
			Condition:      "valueNumber > 55",
			TargetURI:      testServer.URL,
			Status:         "enabled",
			Description:    null.StringFrom("Speed alert webhook"),
			CooldownPeriod: 30,
			DisplayName:    "Speed Alert",
		}

		mockRepo.EXPECT().
			CreateTrigger(gomock.Any(), gomock.Any()).
			Return(expectedTrigger, nil).
			Times(1)

		body, _ := json.Marshal(payload)
		req := httptest.NewRequest(http.MethodPost, "/webhooks", bytes.NewReader(body))
		req.Header.Set("Content-Type", "application/json")

		resp, err := app.Test(req)
		require.NoError(t, err)
		defer resp.Body.Close()

		if !assert.Equal(t, fiber.StatusCreated, resp.StatusCode) {
			body, _ := io.ReadAll(resp.Body)
			t.Log(string(body))
			return
		}

		var response RegisterWebhookResponse
		err = json.NewDecoder(resp.Body).Decode(&response)
		require.NoError(t, err)
		assert.Equal(t, "test-trigger-id", response.ID)
		assert.Equal(t, "Webhook registered successfully", response.Message)
	})

	t.Run("invalid request payload", func(t *testing.T) {
		controller, _, _ := newWebhookControllerAndMocks(t)

		app := newApp()
		devLicense := common.HexToAddress("0x1234567890abcdef")
		app.Use(tokenInjector(devLicense))
		app.Post("/webhooks", controller.RegisterWebhook)

		req := httptest.NewRequest(http.MethodPost, "/webhooks", bytes.NewReader([]byte("invalid json")))
		req.Header.Set("Content-Type", "application/json")

		resp, err := app.Test(req)
		require.NoError(t, err)
		defer resp.Body.Close()

		assert.Equal(t, fiber.StatusBadRequest, resp.StatusCode)
	})

	t.Run("invalid target URL", func(t *testing.T) {
		controller, _, _ := newWebhookControllerAndMocks(t)

		app := newApp()
		devLicense := common.HexToAddress("0x1234567890abcdef")
		app.Use(tokenInjector(devLicense))
		app.Post("/webhooks", controller.RegisterWebhook)

		payload := RegisterWebhookRequest{
			Service:           "telemetry.signals",
			MetricName:        "speed",
			Condition:         "valueNumber > 55",
			CoolDownPeriod:    30,
			TargetURL:         "http://example.com", // Invalid: not HTTPS
			Status:            "enabled",
			VerificationToken: "test-token",
		}

		body, _ := json.Marshal(payload)
		req := httptest.NewRequest(http.MethodPost, "/webhooks", bytes.NewReader(body))
		req.Header.Set("Content-Type", "application/json")

		resp, err := app.Test(req)
		require.NoError(t, err)
		defer resp.Body.Close()

		if !assert.Equal(t, fiber.StatusBadRequest, resp.StatusCode) {
			body, _ := io.ReadAll(resp.Body)
			t.Log(string(body))
			return
		}
	})

	t.Run("invalid service name", func(t *testing.T) {
		controller, _, _ := newWebhookControllerAndMocks(t)

		app := newApp()
		devLicense := common.HexToAddress("0x1234567890abcdef")
		app.Use(tokenInjector(devLicense))
		app.Post("/webhooks", controller.RegisterWebhook)

		payload := RegisterWebhookRequest{
			Service:           "invalid.service",
			MetricName:        "speed",
			Condition:         "valueNumber > 55",
			CoolDownPeriod:    30,
			TargetURL:         "https://example.com",
			Status:            "enabled",
			VerificationToken: "test-token",
		}

		body, _ := json.Marshal(payload)
		req := httptest.NewRequest(http.MethodPost, "/webhooks", bytes.NewReader(body))
		req.Header.Set("Content-Type", "application/json")

		resp, err := app.Test(req)
		require.NoError(t, err)
		defer resp.Body.Close()

		assert.Equal(t, fiber.StatusBadRequest, resp.StatusCode)
	})

	t.Run("webhook verification failure", func(t *testing.T) {
		controller, _, _ := newWebhookControllerAndMocks(t)

		app := newApp()
		devLicense := common.HexToAddress("0x1234567890abcdef")
		app.Use(tokenInjector(devLicense))
		app.Post("/webhooks", controller.RegisterWebhook)

		// Setup test server that returns wrong token
		testServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			w.WriteHeader(http.StatusOK)
			fmt.Fprint(w, "wrong-token")
		}))
		defer testServer.Close()

		payload := RegisterWebhookRequest{
			Service:           "telemetry.signals",
			MetricName:        "speed",
			Condition:         "valueNumber > 55",
			CoolDownPeriod:    30,
			TargetURL:         testServer.URL,
			Status:            "enabled",
			VerificationToken: "expected-token",
		}

		body, _ := json.Marshal(payload)
		req := httptest.NewRequest(http.MethodPost, "/webhooks", bytes.NewReader(body))
		req.Header.Set("Content-Type", "application/json")

		resp, err := app.Test(req)
		require.NoError(t, err)
		defer resp.Body.Close()

		assert.Equal(t, fiber.StatusBadRequest, resp.StatusCode)
	})
}

func TestWebhookController_ListWebhooks(t *testing.T) {
	t.Parallel()

	t.Run("successful list", func(t *testing.T) {
		controller, mockRepo, _ := newWebhookControllerAndMocks(t)

		app := newApp()
		devLicense := common.HexToAddress("0x1234567890abcdef")
		app.Use(tokenInjector(devLicense))
		app.Get("/webhooks", controller.ListWebhooks)

		triggers := []*models.Trigger{
			{
				ID:             "trigger-1",
				Service:        "telemetry.signals",
				MetricName:     "speed",
				Condition:      "valueNumber > 55",
				TargetURI:      "https://example.com/webhook",
				Status:         "enabled",
				Description:    null.StringFrom("Speed alert"),
				CooldownPeriod: 30,
				DisplayName:    "Speed Alert",
				FailureCount:   0,
			},
		}

		mockRepo.EXPECT().
			GetTriggersByDeveloperLicense(gomock.Any(), gomock.Any()).
			Return(triggers, nil).
			Times(1)

		req := httptest.NewRequest(http.MethodGet, "/webhooks", nil)

		resp, err := app.Test(req)
		require.NoError(t, err)
		defer resp.Body.Close()

		assert.Equal(t, fiber.StatusOK, resp.StatusCode)

		var webhooks []WebhookView
		err = json.NewDecoder(resp.Body).Decode(&webhooks)
		require.NoError(t, err)
		assert.Len(t, webhooks, 1)
		assert.Equal(t, "trigger-1", webhooks[0].ID)
		assert.Equal(t, "speed", webhooks[0].MetricName)
	})

	t.Run("empty list", func(t *testing.T) {
		controller, mockRepo, _ := newWebhookControllerAndMocks(t)

		app := newApp()
		devLicense := common.HexToAddress("0x1234567890abcdef")
		app.Use(tokenInjector(devLicense))
		app.Get("/webhooks", controller.ListWebhooks)

		mockRepo.EXPECT().
			GetTriggersByDeveloperLicense(gomock.Any(), gomock.Any()).
			Return([]*models.Trigger{}, nil).
			Times(1)

		req := httptest.NewRequest(http.MethodGet, "/webhooks", nil)

		resp, err := app.Test(req)
		require.NoError(t, err)
		defer resp.Body.Close()

		assert.Equal(t, fiber.StatusOK, resp.StatusCode)

		var webhooks []WebhookView
		err = json.NewDecoder(resp.Body).Decode(&webhooks)
		require.NoError(t, err)
		assert.Len(t, webhooks, 0)
	})
}

func TestWebhookController_UpdateWebhook(t *testing.T) {
	t.Parallel()

	t.Run("successful update", func(t *testing.T) {
		controller, mockRepo, mockCache := newWebhookControllerAndMocks(t)

		app := newApp()
		devLicense := common.HexToAddress("0x1234567890abcdef")
		app.Use(tokenInjector(devLicense))
		app.Put("/webhooks/:webhookId", controller.UpdateWebhook)
		triggerID := uuid.New().String()
		existingTrigger := &models.Trigger{
			ID:             triggerID,
			Service:        "telemetry.signals",
			MetricName:     "speed",
			Condition:      "valueNumber > 55",
			TargetURI:      "https://example.com/webhook",
			Status:         "enabled",
			CooldownPeriod: 30,
			FailureCount:   5,
		}

		newMetricName := vss.FieldPowertrainTransmissionTravelledDistance
		payload := UpdateWebhookRequest{
			MetricName: &newMetricName,
		}

		mockRepo.EXPECT().
			GetTriggerByIDAndDeveloperLicense(gomock.Any(), triggerID, gomock.Any()).
			Return(existingTrigger, nil).
			Times(1)

		mockRepo.EXPECT().
			UpdateTrigger(gomock.Any(), gomock.Any()).
			DoAndReturn(func(ctx context.Context, trigger *models.Trigger) error {
				assert.Equal(t, newMetricName, trigger.MetricName)
				assert.Equal(t, 0, trigger.FailureCount) // Should be reset
				return nil
			}).
			Times(1)

		mockCache.EXPECT().
			PopulateCache(gomock.Any()).
			Return(nil).
			Times(1)

		body, _ := json.Marshal(payload)
		req := httptest.NewRequest(http.MethodPut, "/webhooks/"+triggerID, bytes.NewReader(body))
		req.Header.Set("Content-Type", "application/json")

		resp, err := app.Test(req)
		require.NoError(t, err)
		defer resp.Body.Close()

		assert.Equal(t, fiber.StatusOK, resp.StatusCode)

		var response UpdateWebhookResponse
		err = json.NewDecoder(resp.Body).Decode(&response)
		require.NoError(t, err)
		assert.Equal(t, triggerID, response.ID)
		assert.Equal(t, "Webhook updated successfully", response.Message)
	})
}

func TestWebhookController_DeleteWebhook(t *testing.T) {
	t.Parallel()

	t.Run("successful delete", func(t *testing.T) {
		controller, mockRepo, mockCache := newWebhookControllerAndMocks(t)

		triggerID := uuid.New().String()
		app := newApp()
		devLicense := common.HexToAddress("0x1234567890abcdef")
		app.Use(tokenInjector(devLicense))
		app.Delete("/webhooks/:webhookId", controller.DeleteWebhook)

		mockRepo.EXPECT().
			GetWebhookOwner(gomock.Any(), triggerID).
			Return(common.HexToAddress("0x1234567890abcdef"), nil).
			Times(1)

		mockRepo.EXPECT().
			DeleteTrigger(gomock.Any(), triggerID, gomock.Any()).
			Return(nil).
			Times(1)

		mockCache.EXPECT().
			PopulateCache(gomock.Any()).
			Return(nil).
			Times(1)

		req := httptest.NewRequest(http.MethodDelete, "/webhooks/"+triggerID, nil)

		resp, err := app.Test(req)
		require.NoError(t, err)
		defer resp.Body.Close()

		assert.Equal(t, fiber.StatusOK, resp.StatusCode)

		var response GenericResponse
		err = json.NewDecoder(resp.Body).Decode(&response)
		require.NoError(t, err)
		assert.Equal(t, "Webhook deleted successfully", response.Message)
	})
}

func TestWebhookController_GetSignalNames(t *testing.T) {
	t.Parallel()

	t.Run("successful get signal names", func(t *testing.T) {
		controller, _, _ := newWebhookControllerAndMocks(t)

		app := newApp()
		devLicense := common.HexToAddress("0x1234567890abcdef")
		app.Use(tokenInjector(devLicense))
		app.Get("/signals", controller.GetSignalNames)

		req := httptest.NewRequest(http.MethodGet, "/signals", nil)

		resp, err := app.Test(req)
		require.NoError(t, err)
		defer resp.Body.Close()

		assert.Equal(t, fiber.StatusOK, resp.StatusCode)

		var signals []SignalDefinition
		err = json.NewDecoder(resp.Body).Decode(&signals)
		require.NoError(t, err)
		assert.NotEmpty(t, signals)
	})
}

// Helper function to inject a test token into the fiber context
func tokenInjector(address common.Address) fiber.Handler {
	tok := &auth.Token{
		CustomDexClaims: auth.CustomDexClaims{
			EthereumAddress: address,
		},
	}
	jwtToken := &jwt.Token{
		Claims: tok,
	}
	return func(c *fiber.Ctx) error {
		c.Locals(auth.UserJwtKey, jwtToken)
		return c.Next()
	}
}

func newApp() *fiber.App {
	app := fiber.New(fiber.Config{
		ErrorHandler: func(c *fiber.Ctx, err error) error {
			return fibercommon.ErrorHandler(c, err)
		},
		DisableStartupMessage: true,
	})
	return app
}

func newWebhookControllerAndMocks(t *testing.T) (*WebhookController, *MockRepository, *MockWebhookCache) {
	ctrl := gomock.NewController(t)
	mockRepo := NewMockRepository(ctrl)
	mockCache := NewMockWebhookCache(ctrl)
	controller, err := NewWebhookController(mockRepo, mockCache)
	require.NoError(t, err)
	return controller, mockRepo, mockCache
}
