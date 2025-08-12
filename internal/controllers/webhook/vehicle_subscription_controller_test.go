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
	"testing"

	"github.com/DIMO-Network/vehicle-triggers-api/internal/db/models"
	"github.com/DIMO-Network/vehicle-triggers-api/internal/services/webhookcache"
	"github.com/ericlagergren/decimal"
	"github.com/ethereum/go-ethereum/common"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"github.com/volatiletech/sqlboiler/v4/types"
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
		app.Post("/webhooks/:webhookId/subscribe/:vehicleTokenId", testCtrl.controller.AssignVehicleToWebhook)

		webhookID := "550e8400-e29b-41d4-a716-446655440000"
		vehicleTokenID := big.NewInt(12345)

		// Mock owner check
		testCtrl.mockRepo.EXPECT().
			GetWebhookOwner(gomock.Any(), webhookID).
			Return(devLicense, nil).
			Times(1)

		// Mock permission check
		testCtrl.mockTokenExchange.EXPECT().
			HasVehiclePermissions(gomock.Any(), vehicleTokenID, devLicense, []string{
				"privilege:GetNonLocationHistory",
				"privilege:GetLocationHistory",
			}).
			Return(true, nil).
			Times(1)

		// Mock subscription creation
		expectedSubscription := &models.VehicleSubscription{
			TriggerID:      webhookID,
			VehicleTokenID: decimalFromBigInt(vehicleTokenID),
		}
		testCtrl.mockRepo.EXPECT().
			CreateVehicleSubscription(gomock.Any(), vehicleTokenID, webhookID).
			Return(expectedSubscription, nil).
			Times(1)

		testCtrl.mockCache.EXPECT().
			ScheduleRefresh(gomock.Any()).
			Times(1)

		req := httptest.NewRequest(http.MethodPost, "/webhooks/"+webhookID+"/subscribe/12345", nil)

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
		app.Post("/webhooks/:webhookId/subscribe/:vehicleTokenId", testCtrl.controller.AssignVehicleToWebhook)
		req := httptest.NewRequest(http.MethodPost, "/webhooks/invalid-uuid/subscribe/12345", nil)

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
		app.Post("/webhooks/:webhookId/subscribe/:vehicleTokenId", controller.AssignVehicleToWebhook)

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
		app.Post("/webhooks/:webhookId/subscribe/:vehicleTokenId", controller.AssignVehicleToWebhook)

		webhookID := "550e8400-e29b-41d4-a716-446655440000"

		mockRepo.EXPECT().
			GetWebhookOwner(gomock.Any(), webhookID).
			Return(common.Address{}, sql.ErrNoRows).
			Times(1)

		req := httptest.NewRequest(http.MethodPost, "/webhooks/"+webhookID+"/subscribe/12345", nil)

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
		app.Post("/webhooks/:webhookId/subscribe/:vehicleTokenId", controller.AssignVehicleToWebhook)

		webhookID := "550e8400-e29b-41d4-a716-446655440000"
		vehicleTokenID := big.NewInt(12345)

		// Mock owner check
		mockRepo.EXPECT().
			GetWebhookOwner(gomock.Any(), webhookID).
			Return(devLicense, nil).
			Times(1)

		// Mock permission check - return false for insufficient permissions
		mockTokenExchange.EXPECT().
			HasVehiclePermissions(gomock.Any(), vehicleTokenID, devLicense, []string{
				"privilege:GetNonLocationHistory",
				"privilege:GetLocationHistory",
			}).
			Return(false, nil).
			Times(1)

		req := httptest.NewRequest(http.MethodPost, "/webhooks/"+webhookID+"/subscribe/12345", nil)

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
		vehicleTokenIDs := []*big.Int{big.NewInt(12345), big.NewInt(67890)}

		payload := VehicleListRequest{
			VehicleTokenIDs: vehicleTokenIDs,
		}

		// Mock owner check
		testCtrl.mockRepo.EXPECT().
			GetWebhookOwner(gomock.Any(), webhookID).
			Return(devLicense, nil).
			Times(1)

		// Mock permission checks for both vehicles
		for _, tokenID := range vehicleTokenIDs {
			testCtrl.mockTokenExchange.EXPECT().
				HasVehiclePermissions(gomock.Any(), tokenID, devLicense, []string{
					"privilege:GetNonLocationHistory",
					"privilege:GetLocationHistory",
				}).
				Return(true, nil).
				Times(1)
		}

		// Mock subscription creation for both vehicles
		for _, tokenID := range vehicleTokenIDs {
			expectedSubscription := &models.VehicleSubscription{
				TriggerID:      webhookID,
				VehicleTokenID: decimalFromBigInt(tokenID),
			}
			testCtrl.mockRepo.EXPECT().
				CreateVehicleSubscription(gomock.Any(), tokenID, webhookID).
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
		assert.Equal(t, "Subscribed 2 vehicles", response.Message)
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
		app.Delete("/webhooks/:webhookId/unsubscribe/:vehicleTokenId", testCtrl.controller.RemoveVehicleFromWebhook)

		webhookID := "550e8400-e29b-41d4-a716-446655440000"
		vehicleTokenID := big.NewInt(12345)

		// Mock owner check
		testCtrl.mockRepo.EXPECT().
			GetWebhookOwner(gomock.Any(), webhookID).
			Return(devLicense, nil).
			Times(1)

		// Mock subscription deletion
		testCtrl.mockRepo.EXPECT().
			DeleteVehicleSubscription(gomock.Any(), webhookID, vehicleTokenID).
			Return(int64(1), nil).
			Times(1)

		testCtrl.mockCache.EXPECT().
			ScheduleRefresh(gomock.Any()).
			Times(1)

		req := httptest.NewRequest(http.MethodDelete, "/webhooks/"+webhookID+"/unsubscribe/12345", nil)

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
		vehicleTokenIDs := []*big.Int{big.NewInt(12345), big.NewInt(67890)}

		// Mock owner check
		testCtrl.mockRepo.EXPECT().
			GetWebhookOwner(gomock.Any(), webhookID).
			Return(devLicense, nil).
			Times(1)

		// Mock getting shared vehicles
		testCtrl.mockIdentityAPI.EXPECT().
			GetSharedVehicles(gomock.Any(), devLicense.Bytes()).
			Return(vehicleTokenIDs, nil).
			Times(1)

		// Mock permission checks for both vehicles
		for _, tokenID := range vehicleTokenIDs {
			testCtrl.mockTokenExchange.EXPECT().
				HasVehiclePermissions(gomock.Any(), tokenID, devLicense, []string{
					"privilege:GetNonLocationHistory",
					"privilege:GetLocationHistory",
				}).
				Return(true, nil).
				Times(1)
		}

		// Mock subscription creation for both vehicles
		for _, tokenID := range vehicleTokenIDs {
			expectedSubscription := &models.VehicleSubscription{
				TriggerID:      webhookID,
				VehicleTokenID: decimalFromBigInt(tokenID),
			}
			testCtrl.mockRepo.EXPECT().
				CreateVehicleSubscription(gomock.Any(), tokenID, webhookID).
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
		assert.Equal(t, "Subscribed 2 vehicles", response.Message)
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
		app.Get("/webhooks/vehicles/:vehicleTokenId", testCtrl.controller.ListSubscriptions)

		vehicleTokenID := big.NewInt(12345)

		subscriptions := []*models.VehicleSubscription{
			{
				TriggerID:      "webhook-1",
				VehicleTokenID: decimalFromBigInt(vehicleTokenID),
			},
			{
				TriggerID:      "webhook-2",
				VehicleTokenID: decimalFromBigInt(vehicleTokenID),
			},
		}

		testCtrl.mockRepo.EXPECT().
			GetVehicleSubscriptionsByVehicleAndDeveloperLicense(gomock.Any(), vehicleTokenID, devLicense).
			Return(subscriptions, nil).
			Times(1)

		req := httptest.NewRequest(http.MethodGet, "/webhooks/vehicles/12345", nil)

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

		subscriptions := []*models.VehicleSubscription{
			{
				TriggerID:      webhookID,
				VehicleTokenID: decimalFromBigInt(big.NewInt(12345)),
			},
			{
				TriggerID:      webhookID,
				VehicleTokenID: decimalFromBigInt(big.NewInt(67890)),
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
		assert.Contains(t, vehicleIDs, "12345")
		assert.Contains(t, vehicleIDs, "67890")
	})
}

// Helper function to convert big.Int to types.Decimal for tests
func decimalFromBigInt(value *big.Int) types.Decimal {
	dec := types.NewDecimal(new(decimal.Big))
	dec.SetBigMantScale(value, 0)
	return dec
}
