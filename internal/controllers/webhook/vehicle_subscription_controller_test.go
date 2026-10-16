//go:generate go tool mockgen -source=vehicle_subscription_controller.go -destination=vehicle_subscription_controller_mock_test.go -package=webhook
package webhook

import (
	"bytes"
	"database/sql"
	"encoding/json"
	"errors"
	"math/big"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"

	"github.com/DIMO-Network/cloudevent"
	"github.com/DIMO-Network/model-garage/pkg/vss"
	"github.com/DIMO-Network/vehicle-triggers-api/internal/db/models"
	"github.com/DIMO-Network/vehicle-triggers-api/internal/services/triggersrepo"
	"github.com/DIMO-Network/vehicle-triggers-api/internal/services/webhookcache"
	"github.com/DIMO-Network/vehicle-triggers-api/tests"
	"github.com/ethereum/go-ethereum/common"
	"github.com/gofiber/fiber/v2"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"go.uber.org/mock/gomock"
)

type ControllerWithMocks struct {
	controller        *VehicleSubscriptionController
	mockRepo          *MockRepository
	mockIdentityAPI   *MockIdentityClient
	mockTokenExchange *MockTokenExchangeClient
	mockCache         *MockWebhookCache
}

func NewVehicleSubscriptionControllerAndMocks(t *testing.T) ControllerWithMocks {
	ctrl := gomock.NewController(t)
	mockRepo := NewMockRepository(ctrl)
	mockIdentityAPI := NewMockIdentityClient(ctrl)
	mockTokenExchange := NewMockTokenExchangeClient(ctrl)
	mockCache := NewMockWebhookCache(ctrl)
	subController := NewVehicleSubscriptionController(
		mockRepo,
		mockIdentityAPI,
		mockTokenExchange,
		mockCache,
	)
	return ControllerWithMocks{
		controller:        subController,
		mockRepo:          mockRepo,
		mockIdentityAPI:   mockIdentityAPI,
		mockTokenExchange: mockTokenExchange,
		mockCache:         mockCache,
	}
}

func TestVehicleSubscriptionController_AssignVehicleToWebhook(t *testing.T) {
	t.Parallel()

	t.Run("successful assignment", func(t *testing.T) {
		testCtrl := NewVehicleSubscriptionControllerAndMocks(t)
		app := newApp()
		devLicense := tests.RandomAddr(t)
		app.Use(tokenInjector(devLicense))
		app.Post("/webhooks/:webhookId/subscribe/:assetDID", testCtrl.controller.AssignVehicleToWebhook)

		webhookID := "550e8400-e29b-41d4-a716-446655440000"
		assetDid := cloudevent.ERC721DID{
			ChainID:         137,
			ContractAddress: common.HexToAddress("0xbA5738a18d83D41847dfFbDC6101d37C69c9B0cF"),
			TokenID:         big.NewInt(12345),
		}

		// Mock owner check
		testCtrl.mockRepo.EXPECT().
			GetTriggerByIDAndDeveloperLicense(gomock.Any(), webhookID, devLicense).
			Return(&models.Trigger{
				ID:                      webhookID,
				DeveloperLicenseAddress: devLicense.Bytes(),
				Service:                 triggersrepo.ServiceSignal,
				MetricName:              "vss.speed",
			}, nil).
			Times(1)

		// Mock permission check
		testCtrl.mockTokenExchange.EXPECT().
			HasVehiclePermissions(gomock.Any(), assetDid, devLicense, []string{
				"privilege:GetNonLocationHistory",
			}).
			Return(true, nil).
			Times(1)

		// Mock subscription creation
		expectedSubscription := &models.VehicleSubscription{
			TriggerID: webhookID,
			AssetDid:  assetDid.String(),
		}
		testCtrl.mockRepo.EXPECT().
			CreateVehicleSubscription(gomock.Any(), assetDid, webhookID).
			Return(expectedSubscription, nil).
			Times(1)

		testCtrl.mockCache.EXPECT().
			ScheduleRefresh(gomock.Any()).
			Times(1)
		path, err := url.JoinPath("/webhooks", webhookID, "subscribe", assetDid.String())
		require.NoError(t, err)
		req := httptest.NewRequest(http.MethodPost, path, nil)

		resp, err := app.Test(req)
		require.NoError(t, err)
		defer resp.Body.Close() //nolint:errcheck // fine for tests

		assert.Equal(t, http.StatusCreated, resp.StatusCode)

		var response GenericResponse
		err = json.NewDecoder(resp.Body).Decode(&response)
		require.NoError(t, err)
		assert.Equal(t, "Vehicle assigned successfully", response.Message)
	})

	t.Run("successful assignment with encoded assetDID", func(t *testing.T) {
		testCtrl := NewVehicleSubscriptionControllerAndMocks(t)
		app := newApp()
		devLicense := tests.RandomAddr(t)
		app.Use(tokenInjector(devLicense))
		app.Post("/webhooks/:webhookId/subscribe/:assetDID", testCtrl.controller.AssignVehicleToWebhook)

		webhookID := "550e8400-e29b-41d4-a716-446655440000"
		assetDid := cloudevent.ERC721DID{
			ChainID:         137,
			ContractAddress: common.HexToAddress("0xbA5738a18d83D41847dfFbDC6101d37C69c9B0cF"),
			TokenID:         big.NewInt(12345),
		}

		// Mock owner check
		testCtrl.mockRepo.EXPECT().
			GetTriggerByIDAndDeveloperLicense(gomock.Any(), webhookID, devLicense).
			Return(&models.Trigger{
				ID:                      webhookID,
				DeveloperLicenseAddress: devLicense.Bytes(),
				Service:                 triggersrepo.ServiceSignal,
				MetricName:              vss.FieldCurrentLocationCoordinates,
			}, nil).
			Times(1)

		// Mock permission check
		testCtrl.mockTokenExchange.EXPECT().
			HasVehiclePermissions(gomock.Any(), assetDid, devLicense, []string{
				"privilege:GetLocationHistory",
			}).
			Return(true, nil).
			Times(1)

		// Mock subscription creation
		expectedSubscription := &models.VehicleSubscription{
			TriggerID: webhookID,
			AssetDid:  assetDid.String(),
		}
		testCtrl.mockRepo.EXPECT().
			CreateVehicleSubscription(gomock.Any(), assetDid, webhookID).
			Return(expectedSubscription, nil).
			Times(1)

		testCtrl.mockCache.EXPECT().
			ScheduleRefresh(gomock.Any()).
			Times(1)
		path, err := url.JoinPath("/webhooks", webhookID, "subscribe", url.PathEscape(assetDid.String()))
		require.NoError(t, err)
		req := httptest.NewRequest(http.MethodPost, path, nil)

		resp, err := app.Test(req)
		require.NoError(t, err)
		defer resp.Body.Close() //nolint:errcheck // fine for tests

		assert.Equal(t, http.StatusCreated, resp.StatusCode)

		var response GenericResponse
		err = json.NewDecoder(resp.Body).Decode(&response)
		require.NoError(t, err)
		assert.Equal(t, "Vehicle assigned successfully", response.Message)
	})
	t.Run("invalid webhook ID", func(t *testing.T) {
		testCtrl := NewVehicleSubscriptionControllerAndMocks(t)
		app := newApp()
		devLicense := tests.RandomAddr(t)
		app.Use(tokenInjector(devLicense))
		app.Post("/webhooks/:webhookId/subscribe/:assetDID", testCtrl.controller.AssignVehicleToWebhook)
		req := httptest.NewRequest(http.MethodPost, "/webhooks/invalid-uuid/subscribe/did:erc721:0xbA5738a18d83D41847dfFbDC6101d37C69c9B0cF:12345", nil)

		resp, err := app.Test(req)
		require.NoError(t, err)
		defer resp.Body.Close() //nolint:errcheck // fine for tests

		assert.Equal(t, http.StatusBadRequest, resp.StatusCode)
	})

	t.Run("invalid vehicle token ID", func(t *testing.T) {
		ctrl := gomock.NewController(t)

		mockRepo := NewMockRepository(ctrl)
		mockIdentityAPI := NewMockIdentityClient(ctrl)
		mockTokenExchange := NewMockTokenExchangeClient(ctrl)
		mockCache := &webhookcache.WebhookCache{}

		controller := NewVehicleSubscriptionController(
			mockRepo,
			mockIdentityAPI,
			mockTokenExchange,
			mockCache,
		)

		app := newApp()
		devLicense := tests.RandomAddr(t)
		app.Use(tokenInjector(devLicense))
		app.Post("/webhooks/:webhookId/subscribe/:assetDID", controller.AssignVehicleToWebhook)

		webhookID := "550e8400-e29b-41d4-a716-446655440000"

		req := httptest.NewRequest(http.MethodPost, "/webhooks/"+webhookID+"/subscribe/invalid", nil)

		resp, err := app.Test(req)
		require.NoError(t, err)
		defer resp.Body.Close() //nolint:errcheck // fine for tests

		assert.Equal(t, http.StatusBadRequest, resp.StatusCode)
	})

	t.Run("webhook not found", func(t *testing.T) {
		ctrl := gomock.NewController(t)

		mockRepo := NewMockRepository(ctrl)
		mockIdentityAPI := NewMockIdentityClient(ctrl)
		mockTokenExchange := NewMockTokenExchangeClient(ctrl)
		mockCache := &webhookcache.WebhookCache{}

		controller := NewVehicleSubscriptionController(
			mockRepo,
			mockIdentityAPI,
			mockTokenExchange,
			mockCache,
		)

		app := newApp()
		devLicense := common.HexToAddress("0x1234567890abcdef")
		app.Use(tokenInjector(devLicense))
		app.Post("/webhooks/:webhookId/subscribe/:assetDID", controller.AssignVehicleToWebhook)

		webhookID := "550e8400-e29b-41d4-a716-446655440000"
		assetDid := cloudevent.ERC721DID{
			ChainID:         137,
			ContractAddress: common.HexToAddress("0xbA5738a18d83D41847dfFbDC6101d37C69c9B0cF"),
			TokenID:         big.NewInt(12345),
		}

		mockRepo.EXPECT().
			GetTriggerByIDAndDeveloperLicense(gomock.Any(), webhookID, devLicense).
			Return(nil, sql.ErrNoRows).
			Times(1)

		req := httptest.NewRequest(http.MethodPost, "/webhooks/"+webhookID+"/subscribe/"+assetDid.String(), nil)

		resp, err := app.Test(req)
		require.NoError(t, err)
		defer resp.Body.Close() //nolint:errcheck // fine for tests

		assert.Equal(t, http.StatusNotFound, resp.StatusCode)
	})

	t.Run("insufficient permissions", func(t *testing.T) {
		ctrl := gomock.NewController(t)

		mockRepo := NewMockRepository(ctrl)
		mockIdentityAPI := NewMockIdentityClient(ctrl)
		mockTokenExchange := NewMockTokenExchangeClient(ctrl)
		mockCache := &webhookcache.WebhookCache{}

		controller := NewVehicleSubscriptionController(
			mockRepo,
			mockIdentityAPI,
			mockTokenExchange,
			mockCache,
		)

		app := newApp()
		devLicense := tests.RandomAddr(t)
		app.Use(tokenInjector(devLicense))
		app.Post("/webhooks/:webhookId/subscribe/:assetDID", controller.AssignVehicleToWebhook)

		webhookID := "550e8400-e29b-41d4-a716-446655440000"
		assetDid := cloudevent.ERC721DID{
			ChainID:         137,
			ContractAddress: common.HexToAddress("0xbA5738a18d83D41847dfFbDC6101d37C69c9B0cF"),
			TokenID:         big.NewInt(12345),
		}

		// Mock owner check
		mockRepo.EXPECT().
			GetTriggerByIDAndDeveloperLicense(gomock.Any(), webhookID, devLicense).
			Return(&models.Trigger{
				ID:                      webhookID,
				DeveloperLicenseAddress: devLicense.Bytes(),
				Service:                 triggersrepo.ServiceEvent,
				MetricName:              "behavior.harshBraking",
			}, nil).
			Times(1)

		// Mock permission check - return false for insufficient permissions
		mockTokenExchange.EXPECT().
			HasVehiclePermissions(gomock.Any(), assetDid, devLicense, []string{
				"privilege:GetNonLocationHistory",
				"privilege:GetLocationHistory",
			}).
			Return(false, nil).
			Times(1)

		req := httptest.NewRequest(http.MethodPost, "/webhooks/"+webhookID+"/subscribe/"+assetDid.String(), nil)

		resp, err := app.Test(req)
		require.NoError(t, err)
		defer resp.Body.Close() //nolint:errcheck // fine for tests

		assert.Equal(t, http.StatusForbidden, resp.StatusCode)
	})
}

func TestVehicleSubscriptionController_SubscribeVehiclesFromList(t *testing.T) {
	t.Parallel()

	t.Run("successful subscription", func(t *testing.T) {
		testCtrl := NewVehicleSubscriptionControllerAndMocks(t)

		app := newApp()
		devLicense := tests.RandomAddr(t)
		app.Use(tokenInjector(devLicense))
		app.Post("/webhooks/:webhookId/subscribe/list", testCtrl.controller.SubscribeVehiclesFromList)

		webhookID := "550e8400-e29b-41d4-a716-446655440000"
		assetDIDs := []cloudevent.ERC721DID{
			{
				ChainID:         137,
				ContractAddress: common.HexToAddress("0xbA5738a18d83D41847dfFbDC6101d37C69c9B0cF"),
				TokenID:         big.NewInt(12345),
			},
			{
				ChainID:         137,
				ContractAddress: common.HexToAddress("0xbA5738a18d83D41847dfFbDC6101d37C69c9B0cF"),
				TokenID:         big.NewInt(67890),
			},
		}

		payload := VehicleListRequest{
			AssetDIDs: assetDIDs,
		}

		// Mock owner check
		testCtrl.mockRepo.EXPECT().
			GetTriggerByIDAndDeveloperLicense(gomock.Any(), webhookID, devLicense).
			Return(&models.Trigger{
				ID:                      webhookID,
				DeveloperLicenseAddress: devLicense.Bytes(),
				Service:                 triggersrepo.ServiceSignal,
				MetricName:              "vss.speed",
			}, nil).
			Times(1)

		// Mock permission checks for both vehicles
		for _, assetDid := range assetDIDs {
			testCtrl.mockTokenExchange.EXPECT().
				HasVehiclePermissions(gomock.Any(), assetDid, devLicense, []string{
					"privilege:GetNonLocationHistory",
				}).
				Return(true, nil).
				Times(1)
		}

		// Mock subscription creation for both vehicles
		for _, assetDid := range assetDIDs {
			expectedSubscription := &models.VehicleSubscription{
				TriggerID: webhookID,
				AssetDid:  assetDid.String(),
			}
			testCtrl.mockRepo.EXPECT().
				CreateVehicleSubscription(gomock.Any(), assetDid, webhookID).
				Return(expectedSubscription, nil).
				Times(1)
		}

		testCtrl.mockCache.EXPECT().
			ScheduleRefresh(gomock.Any()).
			Times(1)

		body, _ := json.Marshal(payload)
		req := httptest.NewRequest(http.MethodPost, "/webhooks/"+webhookID+"/subscribe/list", bytes.NewReader(body))
		req.Header.Set("Content-Type", "application/json")

		resp, err := app.Test(req)
		require.NoError(t, err)
		defer resp.Body.Close() //nolint:errcheck // fine for tests

		assert.Equal(t, http.StatusOK, resp.StatusCode)

		var response GenericResponse
		err = json.NewDecoder(resp.Body).Decode(&response)
		require.NoError(t, err)
		assert.Equal(t, "Subscribed 2 assets", response.Message)
	})

	t.Run("invalid request body", func(t *testing.T) {
		testCtrl := NewVehicleSubscriptionControllerAndMocks(t)

		app := newApp()
		devLicense := tests.RandomAddr(t)
		app.Use(tokenInjector(devLicense))
		app.Post("/webhooks/:webhookId/subscribe/list", testCtrl.controller.SubscribeVehiclesFromList)

		webhookID := "550e8400-e29b-41d4-a716-446655440000"

		req := httptest.NewRequest(http.MethodPost, "/webhooks/"+webhookID+"/subscribe/list", bytes.NewReader([]byte("invalid json")))
		req.Header.Set("Content-Type", "application/json")

		resp, err := app.Test(req)
		require.NoError(t, err)
		defer resp.Body.Close() //nolint:errcheck // fine for tests

		assert.Equal(t, http.StatusBadRequest, resp.StatusCode)
	})
}

func TestVehicleSubscriptionController_RemoveVehicleFromWebhook(t *testing.T) {
	t.Parallel()

	t.Run("successful removal", func(t *testing.T) {
		testCtrl := NewVehicleSubscriptionControllerAndMocks(t)

		app := newApp()
		devLicense := tests.RandomAddr(t)
		app.Use(tokenInjector(devLicense))
		app.Delete("/webhooks/:webhookId/unsubscribe/:assetDID", testCtrl.controller.RemoveVehicleFromWebhook)

		webhookID := "550e8400-e29b-41d4-a716-446655440000"
		assetDid := cloudevent.ERC721DID{
			ChainID:         137,
			ContractAddress: common.HexToAddress("0xbA5738a18d83D41847dfFbDC6101d37C69c9B0cF"),
			TokenID:         big.NewInt(12345),
		}

		// Mock owner check
		testCtrl.mockRepo.EXPECT().
			GetTriggerByIDAndDeveloperLicense(gomock.Any(), webhookID, devLicense).
			Return(&models.Trigger{
				ID:                      webhookID,
				DeveloperLicenseAddress: devLicense.Bytes(),
				Service:                 triggersrepo.ServiceSignal,
				MetricName:              "vss.speed",
			}, nil).
			Times(1)

		// Mock subscription deletion
		testCtrl.mockRepo.EXPECT().
			DeleteVehicleSubscription(gomock.Any(), webhookID, assetDid).
			Return(int64(1), nil).
			Times(1)

		testCtrl.mockCache.EXPECT().
			ScheduleRefresh(gomock.Any()).
			Times(1)

		req := httptest.NewRequest(http.MethodDelete, "/webhooks/"+webhookID+"/unsubscribe/"+assetDid.String(), nil)

		resp, err := app.Test(req)
		require.NoError(t, err)
		defer resp.Body.Close() //nolint:errcheck // fine for tests

		assert.Equal(t, http.StatusOK, resp.StatusCode)

		var response GenericResponse
		err = json.NewDecoder(resp.Body).Decode(&response)
		require.NoError(t, err)
		assert.Equal(t, "Vehicle unsubscribed successfully", response.Message)
	})
}

func TestVehicleSubscriptionController_SubscribeAllVehiclesToWebhook(t *testing.T) {
	t.Parallel()

	t.Run("successful subscription to all vehicles", func(t *testing.T) {
		testCtrl := NewVehicleSubscriptionControllerAndMocks(t)

		app := newApp()
		devLicense := tests.RandomAddr(t)
		app.Use(tokenInjector(devLicense))
		app.Post("/webhooks/:webhookId/subscribe/all", testCtrl.controller.SubscribeAllVehiclesToWebhook)

		webhookID := "550e8400-e29b-41d4-a716-446655440000"
		assetDIDs := []cloudevent.ERC721DID{
			{
				ChainID:         137,
				ContractAddress: common.HexToAddress("0xbA5738a18d83D41847dfFbDC6101d37C69c9B0cF"),
				TokenID:         big.NewInt(12345),
			},
			{
				ChainID:         137,
				ContractAddress: common.HexToAddress("0xbA5738a18d83D41847dfFbDC6101d37C69c9B0cF"),
				TokenID:         big.NewInt(67890),
			},
		}

		// Mock owner check
		testCtrl.mockRepo.EXPECT().
			GetTriggerByIDAndDeveloperLicense(gomock.Any(), webhookID, devLicense).
			Return(&models.Trigger{
				ID:                      webhookID,
				DeveloperLicenseAddress: devLicense.Bytes(),
				Service:                 triggersrepo.ServiceEvent,
				MetricName:              "behavior.harshBraking",
			}, nil).
			Times(1)

		// Mock getting shared vehicles
		testCtrl.mockIdentityAPI.EXPECT().
			GetSharedVehicles(gomock.Any(), devLicense.Bytes()).
			Return(assetDIDs, nil).
			Times(1)

		// Mock permission checks for both vehicles
		for _, assetDid := range assetDIDs {
			testCtrl.mockTokenExchange.EXPECT().
				HasVehiclePermissions(gomock.Any(), assetDid, devLicense, []string{
					"privilege:GetNonLocationHistory",
					"privilege:GetLocationHistory",
				}).
				Return(true, nil).
				Times(1)
		}

		// Mock subscription creation for both vehicles
		for _, assetDid := range assetDIDs {
			expectedSubscription := &models.VehicleSubscription{
				TriggerID: webhookID,
				AssetDid:  assetDid.String(),
			}
			testCtrl.mockRepo.EXPECT().
				CreateVehicleSubscription(gomock.Any(), assetDid, webhookID).
				Return(expectedSubscription, nil).
				Times(1)
		}
		testCtrl.mockCache.EXPECT().
			ScheduleRefresh(gomock.Any()).
			Times(1)

		req := httptest.NewRequest(http.MethodPost, "/webhooks/"+webhookID+"/subscribe/all", nil)

		resp, err := app.Test(req)
		require.NoError(t, err)
		defer resp.Body.Close() //nolint:errcheck // fine for tests

		assert.Equal(t, http.StatusOK, resp.StatusCode)

		var response GenericResponse
		err = json.NewDecoder(resp.Body).Decode(&response)
		require.NoError(t, err)
		assert.Equal(t, "Subscribed 2 assets", response.Message)
	})

	t.Run("identity API error", func(t *testing.T) {
		testCtrl := NewVehicleSubscriptionControllerAndMocks(t)

		app := newApp()
		devLicense := tests.RandomAddr(t)
		app.Use(tokenInjector(devLicense))
		app.Post("/webhooks/:webhookId/subscribe/all", testCtrl.controller.SubscribeAllVehiclesToWebhook)

		webhookID := "550e8400-e29b-41d4-a716-446655440000"

		// Mock owner check
		testCtrl.mockRepo.EXPECT().
			GetTriggerByIDAndDeveloperLicense(gomock.Any(), webhookID, devLicense).
			Return(&models.Trigger{
				ID:                      webhookID,
				DeveloperLicenseAddress: devLicense.Bytes(),
				Service:                 triggersrepo.ServiceSignal,
				MetricName:              "vss.speed",
			}, nil).
			Times(1)

		// Mock getting shared vehicles - return error
		testCtrl.mockIdentityAPI.EXPECT().
			GetSharedVehicles(gomock.Any(), devLicense.Bytes()).
			Return(nil, errors.New("identity API error")).
			Times(1)

		req := httptest.NewRequest(http.MethodPost, "/webhooks/"+webhookID+"/subscribe/all", nil)

		resp, err := app.Test(req)
		require.NoError(t, err)
		defer resp.Body.Close() //nolint:errcheck // fine for tests

		assert.Equal(t, http.StatusInternalServerError, resp.StatusCode)
	})
}

func TestVehicleSubscriptionController_UnsubscribeAllVehiclesFromWebhook(t *testing.T) {
	t.Parallel()

	t.Run("successful unsubscription", func(t *testing.T) {
		testCtrl := NewVehicleSubscriptionControllerAndMocks(t)
		app := newApp()
		devLicense := tests.RandomAddr(t)
		app.Use(tokenInjector(devLicense))
		app.Delete("/webhooks/:webhookId/unsubscribe/all", testCtrl.controller.UnsubscribeAllVehiclesFromWebhook)

		webhookID := "550e8400-e29b-41d4-a716-446655440000"

		// Mock owner check
		testCtrl.mockRepo.EXPECT().
			GetTriggerByIDAndDeveloperLicense(gomock.Any(), webhookID, devLicense).
			Return(&models.Trigger{
				ID:                      webhookID,
				DeveloperLicenseAddress: devLicense.Bytes(),
				Service:                 triggersrepo.ServiceSignal,
				MetricName:              "vss.speed",
			}, nil).
			Times(1)

		// Mock deleting all subscriptions
		testCtrl.mockRepo.EXPECT().
			DeleteAllVehicleSubscriptionsForTrigger(gomock.Any(), webhookID).
			Return(int64(5), nil). // 5 vehicles unsubscribed
			Times(1)

		testCtrl.mockCache.EXPECT().
			ScheduleRefresh(gomock.Any()).
			Times(1)

		req := httptest.NewRequest(http.MethodDelete, "/webhooks/"+webhookID+"/unsubscribe/all", nil)

		resp, err := app.Test(req)
		require.NoError(t, err)
		defer resp.Body.Close() //nolint:errcheck // fine for tests

		assert.Equal(t, http.StatusOK, resp.StatusCode)

		var response GenericResponse
		err = json.NewDecoder(resp.Body).Decode(&response)
		require.NoError(t, err)
		assert.Equal(t, "Unsubscribed 5 vehicles", response.Message)
	})
}

func TestVehicleSubscriptionController_ListSubscriptions(t *testing.T) {
	t.Parallel()

	t.Run("successful list", func(t *testing.T) {
		testCtrl := NewVehicleSubscriptionControllerAndMocks(t)

		app := newApp()
		devLicense := tests.RandomAddr(t)
		app.Use(tokenInjector(devLicense))
		app.Get("/webhooks/vehicles/:assetDID", testCtrl.controller.ListSubscriptions)

		assetDid := cloudevent.ERC721DID{
			ChainID:         137,
			ContractAddress: common.HexToAddress("0xbA5738a18d83D41847dfFbDC6101d37C69c9B0cF"),
			TokenID:         big.NewInt(12345),
		}

		subscriptions := []*models.VehicleSubscription{
			{
				TriggerID: "webhook-1",
				AssetDid:  assetDid.String(),
			},
			{
				TriggerID: "webhook-2",
				AssetDid:  assetDid.String(),
			},
		}

		testCtrl.mockRepo.EXPECT().
			GetVehicleSubscriptionsByVehicleAndDeveloperLicense(gomock.Any(), assetDid, devLicense).
			Return(subscriptions, nil).
			Times(1)

		req := httptest.NewRequest(http.MethodGet, "/webhooks/vehicles/"+assetDid.String(), nil)

		resp, err := app.Test(req)
		require.NoError(t, err)
		defer resp.Body.Close() //nolint:errcheck // fine for tests

		assert.Equal(t, http.StatusOK, resp.StatusCode)

		var subscriptionViews []SubscriptionView
		err = json.NewDecoder(resp.Body).Decode(&subscriptionViews)
		require.NoError(t, err)
		assert.Len(t, subscriptionViews, 2)
		assert.Equal(t, "webhook-1", subscriptionViews[0].WebhookID)
		assert.Equal(t, "webhook-2", subscriptionViews[1].WebhookID)
	})
}

func TestVehicleSubscriptionController_ListVehiclesForWebhook(t *testing.T) {
	t.Parallel()

	t.Run("successful list", func(t *testing.T) {
		testCtrl := NewVehicleSubscriptionControllerAndMocks(t)

		app := newApp()
		devLicense := tests.RandomAddr(t)
		app.Use(tokenInjector(devLicense))
		app.Get("/webhooks/:webhookId", testCtrl.controller.ListVehiclesForWebhook)

		webhookID := "550e8400-e29b-41d4-a716-446655440000"

		assetDid1 := cloudevent.ERC721DID{
			ChainID:         137,
			ContractAddress: common.HexToAddress("0xbA5738a18d83D41847dfFbDC6101d37C69c9B0cF"),
			TokenID:         big.NewInt(12345),
		}
		assetDid2 := cloudevent.ERC721DID{
			ChainID:         137,
			ContractAddress: common.HexToAddress("0xbA5738a18d83D41847dfFbDC6101d37C69c9B0cF"),
			TokenID:         big.NewInt(67890),
		}
		subscriptions := []*models.VehicleSubscription{
			{
				TriggerID: webhookID,
				AssetDid:  assetDid1.String(),
			},
			{
				TriggerID: webhookID,
				AssetDid:  assetDid2.String(),
			},
		}

		// Mock owner check
		testCtrl.mockRepo.EXPECT().
			GetTriggerByIDAndDeveloperLicense(gomock.Any(), webhookID, devLicense).
			Return(&models.Trigger{
				ID:                      webhookID,
				DeveloperLicenseAddress: devLicense.Bytes(),
				Service:                 triggersrepo.ServiceSignal,
				MetricName:              "vss.speed",
			}, nil).
			Times(1)

		testCtrl.mockRepo.EXPECT().
			GetVehicleSubscriptionsByTriggerID(gomock.Any(), webhookID).
			Return(subscriptions, nil).
			Times(1)

		req := httptest.NewRequest(http.MethodGet, "/webhooks/"+webhookID, nil)

		resp, err := app.Test(req)
		require.NoError(t, err)
		defer resp.Body.Close() //nolint:errcheck // fine for tests

		assert.Equal(t, http.StatusOK, resp.StatusCode)

		var vehicleIDs []string
		err = json.NewDecoder(resp.Body).Decode(&vehicleIDs)
		require.NoError(t, err)
		assert.Len(t, vehicleIDs, 2)
		assert.Contains(t, vehicleIDs, assetDid1.String())
		assert.Contains(t, vehicleIDs, assetDid2.String())
	})
}

func TestWebhookChangesRecordTheSigner(t *testing.T) {
	t.Parallel()
	webhookID := "550e8400-e29b-41d4-a716-446655440000"
	devLicense := common.HexToAddress("0x1234567890abcdef")
	assetDid := cloudevent.ERC721DID{
		ChainID:         137,
		ContractAddress: common.HexToAddress("0xbA5738a18d83D41847dfFbDC6101d37C69c9B0cF"),
		TokenID:         big.NewInt(12345),
	}
	listBody, err := json.Marshal(VehicleListRequest{AssetDIDs: []cloudevent.ERC721DID{assetDid}})
	require.NoError(t, err)

	type route struct {
		method, path string
		body         []byte
		handler      func(c ControllerWithMocks) fiber.Handler
		mutation     func(c ControllerWithMocks) *gomock.Call
		before       func(c ControllerWithMocks)
	}
	permitted := func(c ControllerWithMocks) {
		c.mockTokenExchange.EXPECT().HasVehiclePermissions(gomock.Any(), assetDid, devLicense, gomock.Any()).Return(true, nil)
	}
	routes := map[string]route{
		"subscribe one": {
			method: http.MethodPost, path: "/webhooks/:webhookId/subscribe/:assetDID",
			handler: func(c ControllerWithMocks) fiber.Handler { return c.controller.AssignVehicleToWebhook },
			before:  permitted,
			mutation: func(c ControllerWithMocks) *gomock.Call {
				return c.mockRepo.EXPECT().CreateVehicleSubscription(gomock.Any(), assetDid, webhookID).Return(&models.VehicleSubscription{}, nil)
			},
		},
		"subscribe list": {
			method: http.MethodPost, path: "/webhooks/:webhookId/subscribe/list", body: listBody,
			handler: func(c ControllerWithMocks) fiber.Handler { return c.controller.SubscribeVehiclesFromList },
			before:  permitted,
			mutation: func(c ControllerWithMocks) *gomock.Call {
				return c.mockRepo.EXPECT().CreateVehicleSubscription(gomock.Any(), assetDid, webhookID).Return(&models.VehicleSubscription{}, nil)
			},
		},
		"subscribe all": {
			method: http.MethodPost, path: "/webhooks/:webhookId/subscribe/all",
			handler: func(c ControllerWithMocks) fiber.Handler { return c.controller.SubscribeAllVehiclesToWebhook },
			before: func(c ControllerWithMocks) {
				c.mockIdentityAPI.EXPECT().GetSharedVehicles(gomock.Any(), devLicense.Bytes()).Return([]cloudevent.ERC721DID{assetDid}, nil)
				permitted(c)
			},
			mutation: func(c ControllerWithMocks) *gomock.Call {
				return c.mockRepo.EXPECT().CreateVehicleSubscription(gomock.Any(), assetDid, webhookID).Return(&models.VehicleSubscription{}, nil)
			},
		},
		"unsubscribe one": {
			method: http.MethodDelete, path: "/webhooks/:webhookId/unsubscribe/:assetDID",
			handler: func(c ControllerWithMocks) fiber.Handler { return c.controller.RemoveVehicleFromWebhook },
			mutation: func(c ControllerWithMocks) *gomock.Call {
				return c.mockRepo.EXPECT().DeleteVehicleSubscription(gomock.Any(), webhookID, assetDid).Return(int64(1), nil)
			},
		},
		"unsubscribe list": {
			method: http.MethodDelete, path: "/webhooks/:webhookId/unsubscribe/list", body: listBody,
			handler: func(c ControllerWithMocks) fiber.Handler { return c.controller.UnsubscribeVehiclesFromList },
			mutation: func(c ControllerWithMocks) *gomock.Call {
				return c.mockRepo.EXPECT().DeleteVehicleSubscription(gomock.Any(), webhookID, assetDid).Return(int64(1), nil)
			},
		},
		"unsubscribe all": {
			method: http.MethodDelete, path: "/webhooks/:webhookId/unsubscribe/all",
			handler: func(c ControllerWithMocks) fiber.Handler { return c.controller.UnsubscribeAllVehiclesFromWebhook },
			mutation: func(c ControllerWithMocks) *gomock.Call {
				return c.mockRepo.EXPECT().DeleteAllVehicleSubscriptionsForTrigger(gomock.Any(), webhookID).Return(int64(1), nil)
			},
		},
	}

	for name, r := range routes {
		t.Run(name, func(t *testing.T) {
			c := NewVehicleSubscriptionControllerAndMocks(t)
			app := newApp()
			app.Use(tokenInjectorWithSigner(devLicense, testSigner))
			app.Add(r.method, r.path, r.handler(c))

			c.mockRepo.EXPECT().GetTriggerByIDAndDeveloperLicense(gomock.Any(), webhookID, devLicense).Return(&models.Trigger{
				ID: webhookID, DeveloperLicenseAddress: devLicense.Bytes(), Service: triggersrepo.ServiceSignal, MetricName: "vss.speed",
			}, nil)
			if r.before != nil {
				r.before(c)
			}
			gomock.InOrder(
				c.mockRepo.EXPECT().SetTriggerUpdatedBySigner(gomock.Any(), webhookID, testSigner).Return(nil),
				r.mutation(c),
			)
			c.mockCache.EXPECT().ScheduleRefresh(gomock.Any())

			path := strings.NewReplacer(":webhookId", webhookID, ":assetDID", url.PathEscape(assetDid.String())).Replace(r.path)
			req := httptest.NewRequest(r.method, path, bytes.NewReader(r.body))
			req.Header.Set("Content-Type", "application/json")
			resp, err := app.Test(req)
			require.NoError(t, err)
			require.Less(t, resp.StatusCode, 300)
		})
	}

	t.Run("a claimless change records nothing", func(t *testing.T) {
		c := NewVehicleSubscriptionControllerAndMocks(t)
		app := newApp()
		app.Use(tokenInjector(devLicense))
		app.Delete("/webhooks/:webhookId/unsubscribe/all", c.controller.UnsubscribeAllVehiclesFromWebhook)
		c.mockRepo.EXPECT().GetTriggerByIDAndDeveloperLicense(gomock.Any(), webhookID, devLicense).Return(&models.Trigger{
			ID: webhookID, DeveloperLicenseAddress: devLicense.Bytes(),
		}, nil)
		c.mockRepo.EXPECT().DeleteAllVehicleSubscriptionsForTrigger(gomock.Any(), webhookID).Return(int64(1), nil)
		c.mockCache.EXPECT().ScheduleRefresh(gomock.Any())

		resp, err := app.Test(httptest.NewRequest(http.MethodDelete, "/webhooks/"+webhookID+"/unsubscribe/all", nil))
		require.NoError(t, err)
		require.Equal(t, fiber.StatusOK, resp.StatusCode)
	})
}
