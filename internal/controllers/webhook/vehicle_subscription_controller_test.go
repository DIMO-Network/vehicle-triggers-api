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
	"testing"

	"github.com/DIMO-Network/cloudevent"
	"github.com/DIMO-Network/vehicle-triggers-api/internal/db/models"
	"github.com/DIMO-Network/vehicle-triggers-api/internal/services/webhookcache"
	"github.com/ethereum/go-ethereum/common"
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
		devLicense := common.HexToAddress("0x1234567890abcdef")
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
			GetWebhookOwner(gomock.Any(), webhookID).
			Return(devLicense, nil).
			Times(1)

		// Mock permission check
		testCtrl.mockTokenExchange.EXPECT().
			HasVehiclePermissions(gomock.Any(), assetDid, devLicense, []string{
				"privilege:GetNonLocationHistory",
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
		devLicense := common.HexToAddress("0x1234567890abcdef")
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
			GetWebhookOwner(gomock.Any(), webhookID).
			Return(devLicense, nil).
			Times(1)

		// Mock permission check
		testCtrl.mockTokenExchange.EXPECT().
			HasVehiclePermissions(gomock.Any(), assetDid, devLicense, []string{
				"privilege:GetNonLocationHistory",
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
		devLicense := common.HexToAddress("0x1234567890abcdef")
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
		devLicense := common.HexToAddress("0x1234567890abcdef")
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
			GetWebhookOwner(gomock.Any(), webhookID).
			Return(common.Address{}, sql.ErrNoRows).
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
		devLicense := common.HexToAddress("0x1234567890abcdef")
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
			GetWebhookOwner(gomock.Any(), webhookID).
			Return(devLicense, nil).
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
		devLicense := common.HexToAddress("0x1234567890abcdef")
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
			GetWebhookOwner(gomock.Any(), webhookID).
			Return(devLicense, nil).
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
		devLicense := common.HexToAddress("0x1234567890abcdef")
		app.Use(tokenInjector(devLicense))
		app.Post("/webhooks/:webhookId/subscribe/list", testCtrl.controller.SubscribeVehiclesFromList)

		webhookID := "550e8400-e29b-41d4-a716-446655440000"

		// Mock owner check
		testCtrl.mockRepo.EXPECT().
			GetWebhookOwner(gomock.Any(), webhookID).
			Return(devLicense, nil).
			Times(1)

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
		devLicense := common.HexToAddress("0x1234567890abcdef")
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
			GetWebhookOwner(gomock.Any(), webhookID).
			Return(devLicense, nil).
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
		devLicense := common.HexToAddress("0x1234567890abcdef")
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
			GetWebhookOwner(gomock.Any(), webhookID).
			Return(devLicense, nil).
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
		devLicense := common.HexToAddress("0x1234567890abcdef")
		app.Use(tokenInjector(devLicense))
		app.Post("/webhooks/:webhookId/subscribe/all", testCtrl.controller.SubscribeAllVehiclesToWebhook)

		webhookID := "550e8400-e29b-41d4-a716-446655440000"

		// Mock owner check
		testCtrl.mockRepo.EXPECT().
			GetWebhookOwner(gomock.Any(), webhookID).
			Return(devLicense, nil).
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
		devLicense := common.HexToAddress("0x1234567890abcdef")
		app.Use(tokenInjector(devLicense))
		app.Delete("/webhooks/:webhookId/unsubscribe/all", testCtrl.controller.UnsubscribeAllVehiclesFromWebhook)

		webhookID := "550e8400-e29b-41d4-a716-446655440000"

		// Mock owner check
		testCtrl.mockRepo.EXPECT().
			GetWebhookOwner(gomock.Any(), webhookID).
			Return(devLicense, nil).
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
		devLicense := common.HexToAddress("0x1234567890abcdef")
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
		devLicense := common.HexToAddress("0x1234567890abcdef")
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
			GetWebhookOwner(gomock.Any(), webhookID).
			Return(devLicense, nil).
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
