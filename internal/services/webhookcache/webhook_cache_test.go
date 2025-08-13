//go:generate go tool mockgen -source=webhook_cache.go -destination=webhook_cache_mock_test.go -package=webhookcache
package webhookcache

import (
	"context"
	"crypto/rand"
	"errors"
	"math/big"
	"testing"

	"github.com/DIMO-Network/cloudevent"
	"github.com/DIMO-Network/vehicle-triggers-api/internal/config"
	"github.com/DIMO-Network/vehicle-triggers-api/internal/db/models"
	"github.com/DIMO-Network/vehicle-triggers-api/internal/services/triggersrepo"
	"github.com/ethereum/go-ethereum/common"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"go.uber.org/mock/gomock"
)

func TestWebhookCache_PopulateCache(t *testing.T) {
	t.Parallel()

	t.Run("successful population", func(t *testing.T) {
		ctrl := gomock.NewController(t)
		defer ctrl.Finish()

		mockRepo := NewMockRepository(ctrl)
		cache := NewWebhookCache(mockRepo, &config.Settings{})
		ctx := context.Background()

		// Create test data
		assetDid1 := randAssetDID(t)
		assetDid2 := randAssetDID(t)

		subs := []*models.VehicleSubscription{
			{
				AssetDid:  assetDid1.String(),
				TriggerID: "trigger-1",
			},
			{
				AssetDid:  assetDid2.String(),
				TriggerID: "trigger-1",
			},
			{
				AssetDid:  assetDid1.String(),
				TriggerID: "trigger-2",
			},
		}

		trigger1 := &models.Trigger{
			ID:         "trigger-1",
			MetricName: "speed",
			Status:     triggersrepo.StatusEnabled,
			Condition:  "valueNumber > 10",
		}

		trigger2 := &models.Trigger{
			ID:         "trigger-2",
			MetricName: "temperature",
			Status:     triggersrepo.StatusEnabled,
			Condition:  "valueNumber > 10",
		}

		// Set expectations
		mockRepo.EXPECT().
			InternalGetAllVehicleSubscriptions(ctx).
			Return(subs, nil).
			Times(1)

		mockRepo.EXPECT().
			InternalGetTriggerByID(ctx, "trigger-1").
			Return(trigger1, nil).
			Times(1)

		mockRepo.EXPECT().
			InternalGetTriggerByID(ctx, "trigger-2").
			Return(trigger2, nil).
			Times(1)

		// Execute
		err := cache.PopulateCache(ctx)

		// Verify
		require.NoError(t, err)

		// Check that cache was populated correctly
		webhooks := cache.GetWebhooks(assetDid1.String(), "speed")
		require.Len(t, webhooks, 1)
		assert.Equal(t, "trigger-1", webhooks[0].Trigger.ID)

		webhooks = cache.GetWebhooks(assetDid2.String(), "speed")
		require.Len(t, webhooks, 1)
		assert.Equal(t, "trigger-1", webhooks[0].Trigger.ID)

		webhooks = cache.GetWebhooks(assetDid1.String(), "temperature")
		require.Len(t, webhooks, 1)
		assert.Equal(t, "trigger-2", webhooks[0].Trigger.ID)

		webhooks = cache.GetWebhooks(assetDid2.String(), "temperature")
		require.Nil(t, webhooks)
	})

	t.Run("handles no webhook configurations", func(t *testing.T) {
		ctrl := gomock.NewController(t)
		defer ctrl.Finish()

		mockRepo := NewMockRepository(ctrl)
		cache := NewWebhookCache(mockRepo, &config.Settings{})
		ctx := context.Background()

		// Return empty subscriptions
		mockRepo.EXPECT().
			InternalGetAllVehicleSubscriptions(ctx).
			Return([]*models.VehicleSubscription{}, nil).
			Times(1)

		// Execute
		err := cache.PopulateCache(ctx)

		// Verify
		require.NoError(t, err)

		// Cache should be empty
		webhooks := cache.GetWebhooks(randAssetDID(t).String(), "speed")
		assert.Nil(t, webhooks)
	})

	t.Run("handles database error when getting subscriptions", func(t *testing.T) {
		ctrl := gomock.NewController(t)
		defer ctrl.Finish()

		mockRepo := NewMockRepository(ctrl)
		cache := NewWebhookCache(mockRepo, &config.Settings{})
		ctx := context.Background()

		expectedErr := errors.New("database error")
		mockRepo.EXPECT().
			InternalGetAllVehicleSubscriptions(ctx).
			Return(nil, expectedErr).
			Times(1)

		// Execute
		err := cache.PopulateCache(ctx)

		// Verify
		require.Error(t, err)
		assert.ErrorIs(t, err, expectedErr)
	})

	t.Run("handles error when getting trigger", func(t *testing.T) {
		ctrl := gomock.NewController(t)
		defer ctrl.Finish()

		mockRepo := NewMockRepository(ctrl)
		cache := NewWebhookCache(mockRepo, &config.Settings{})
		ctx := context.Background()

		assetDid := randAssetDID(t)
		subs := []*models.VehicleSubscription{
			{
				AssetDid:  assetDid.String(),
				TriggerID: "trigger-1",
			},
		}

		expectedErr := errors.New("trigger not found")
		mockRepo.EXPECT().
			InternalGetAllVehicleSubscriptions(ctx).
			Return(subs, nil).
			Times(1)

		mockRepo.EXPECT().
			InternalGetTriggerByID(ctx, "trigger-1").
			Return(nil, expectedErr).
			Times(1)

		// Execute
		err := cache.PopulateCache(ctx)

		// Verify - should not error but should skip the problematic trigger
		require.NoError(t, err)

		// Cache should be empty since trigger couldn't be loaded
		webhooks := cache.GetWebhooks(assetDid.String(), "speed")
		assert.Nil(t, webhooks)
	})

	t.Run("skips disabled triggers", func(t *testing.T) {
		ctrl := gomock.NewController(t)
		defer ctrl.Finish()

		mockRepo := NewMockRepository(ctrl)
		cache := NewWebhookCache(mockRepo, &config.Settings{})
		ctx := context.Background()

		assetDid := randAssetDID(t)
		subs := []*models.VehicleSubscription{
			{
				AssetDid:  assetDid.String(),
				TriggerID: "trigger-1",
			},
		}

		disabledTrigger := &models.Trigger{
			ID:         "trigger-1",
			MetricName: "speed",
			Status:     triggersrepo.StatusDisabled,
			Condition:  "valueNumber > 10",
		}

		mockRepo.EXPECT().
			InternalGetAllVehicleSubscriptions(ctx).
			Return(subs, nil).
			Times(1)

		mockRepo.EXPECT().
			InternalGetTriggerByID(ctx, "trigger-1").
			Return(disabledTrigger, nil).
			Times(1)

		// Execute
		err := cache.PopulateCache(ctx)

		// Verify
		require.NoError(t, err)

		// Cache should be empty since trigger is disabled
		webhooks := cache.GetWebhooks(assetDid.String(), "speed")
		assert.Nil(t, webhooks)
	})

	t.Run("handles multiple triggers with same metric name", func(t *testing.T) {
		ctrl := gomock.NewController(t)
		defer ctrl.Finish()

		mockRepo := NewMockRepository(ctrl)
		cache := NewWebhookCache(mockRepo, &config.Settings{})
		ctx := context.Background()

		assetDid := randAssetDID(t)
		subs := []*models.VehicleSubscription{
			{
				AssetDid:  assetDid.String(),
				TriggerID: "trigger-1",
			},
			{
				AssetDid:  assetDid.String(),
				TriggerID: "trigger-2",
			},
		}

		trigger1 := &models.Trigger{
			ID:         "trigger-1",
			MetricName: "speed",
			Status:     triggersrepo.StatusEnabled,
			Condition:  "valueNumber > 10",
		}

		trigger2 := &models.Trigger{
			ID:         "trigger-2",
			MetricName: "speed", // Same metric name
			Status:     triggersrepo.StatusEnabled,
			Condition:  "valueNumber > 10",
		}

		mockRepo.EXPECT().
			InternalGetAllVehicleSubscriptions(ctx).
			Return(subs, nil).
			Times(1)

		mockRepo.EXPECT().
			InternalGetTriggerByID(ctx, "trigger-1").
			Return(trigger1, nil).
			Times(1)

		mockRepo.EXPECT().
			InternalGetTriggerByID(ctx, "trigger-2").
			Return(trigger2, nil).
			Times(1)

		// Execute
		err := cache.PopulateCache(ctx)

		// Verify
		require.NoError(t, err)

		// Should have both triggers for the same metric
		webhooks := cache.GetWebhooks(assetDid.String(), "speed")
		require.Len(t, webhooks, 2)

		triggerIDs := []string{webhooks[0].Trigger.ID, webhooks[1].Trigger.ID}
		assert.Contains(t, triggerIDs, "trigger-1")
		assert.Contains(t, triggerIDs, "trigger-2")
	})

	t.Run("shares trigger objects for memory efficiency", func(t *testing.T) {
		ctrl := gomock.NewController(t)
		defer ctrl.Finish()

		mockRepo := NewMockRepository(ctrl)
		cache := NewWebhookCache(mockRepo, &config.Settings{})
		ctx := context.Background()

		assetDid1 := randAssetDID(t)
		assetDid2 := randAssetDID(t)

		// Multiple subscriptions for same trigger
		subs := []*models.VehicleSubscription{
			{
				AssetDid:  assetDid1.String(),
				TriggerID: "trigger-1",
			},
			{
				AssetDid:  assetDid2.String(),
				TriggerID: "trigger-1",
			},
		}

		trigger1 := &models.Trigger{
			ID:         "trigger-1",
			MetricName: "speed",
			Status:     triggersrepo.StatusEnabled,
			Condition:  "valueNumber > 10",
		}

		mockRepo.EXPECT().
			InternalGetAllVehicleSubscriptions(ctx).
			Return(subs, nil).
			Times(1)

		// Should only be called once due to caching
		mockRepo.EXPECT().
			InternalGetTriggerByID(ctx, "trigger-1").
			Return(trigger1, nil).
			Times(1)

		// Execute
		err := cache.PopulateCache(ctx)

		// Verify
		require.NoError(t, err)

		webhooks1 := cache.GetWebhooks(assetDid1.String(), "speed")
		webhooks2 := cache.GetWebhooks(assetDid2.String(), "speed")

		require.Len(t, webhooks1, 1)
		require.Len(t, webhooks2, 1)

		// Should be the exact same object reference
		assert.True(t, webhooks1[0] == webhooks2[0], "Expected same trigger object reference")
	})
}

func TestWebhookCache_GetWebhooks(t *testing.T) {
	t.Parallel()

	t.Run("returns webhooks for existing vehicle and metric", func(t *testing.T) {
		ctrl := gomock.NewController(t)
		defer ctrl.Finish()

		mockRepo := NewMockRepository(ctrl)
		cache := NewWebhookCache(mockRepo, &config.Settings{})

		// Manually populate cache
		trigger := &models.Trigger{
			ID:         "trigger-1",
			MetricName: "speed",
			Status:     triggersrepo.StatusEnabled,
			Condition:  "valueNumber > 10",
		}

		assetDid := randAssetDID(t)
		testData := map[string]map[string][]*Webhook{
			assetDid.String(): {
				"speed": []*Webhook{
					{
						Trigger: trigger,
						Program: nil,
					},
				},
			},
		}

		cache.Update(testData)

		// Test
		webhooks := cache.GetWebhooks(assetDid.String(), "speed")

		// Verify
		require.Len(t, webhooks, 1)
		assert.Equal(t, "trigger-1", webhooks[0].Trigger.ID)
	})

	t.Run("returns nil for non-existent vehicle", func(t *testing.T) {
		ctrl := gomock.NewController(t)
		defer ctrl.Finish()

		mockRepo := NewMockRepository(ctrl)
		cache := NewWebhookCache(mockRepo, &config.Settings{})

		// Test with empty cache
		webhooks := cache.GetWebhooks(randAssetDID(t).String(), "speed")

		// Verify
		assert.Nil(t, webhooks)
	})

	t.Run("returns nil for non-existent metric", func(t *testing.T) {
		ctrl := gomock.NewController(t)
		defer ctrl.Finish()

		mockRepo := NewMockRepository(ctrl)
		cache := NewWebhookCache(mockRepo, &config.Settings{})

		// Manually populate cache with different metric
		trigger := &models.Trigger{
			ID:         "trigger-1",
			MetricName: "temperature",
			Status:     triggersrepo.StatusEnabled,
			Condition:  "valueNumber > 10",
		}

		assetDid := randAssetDID(t)
		testData := map[string]map[string][]*Webhook{
			assetDid.String(): {
				"temperature": []*Webhook{
					{
						Trigger: trigger,
						Program: nil,
					},
				},
			},
		}

		cache.Update(testData)

		// Test with different metric
		webhooks := cache.GetWebhooks(assetDid.String(), "speed")

		// Verify
		assert.Nil(t, webhooks)
	})

	t.Run("returns multiple webhooks for same vehicle and metric", func(t *testing.T) {
		ctrl := gomock.NewController(t)
		defer ctrl.Finish()

		mockRepo := NewMockRepository(ctrl)
		cache := NewWebhookCache(mockRepo, &config.Settings{})

		// Create multiple triggers for same metric
		trigger1 := &models.Trigger{
			ID:         "trigger-1",
			MetricName: "speed",
			Status:     triggersrepo.StatusEnabled,
			Condition:  "valueNumber > 10",
		}

		trigger2 := &models.Trigger{
			ID:         "trigger-2",
			MetricName: "speed",
			Status:     triggersrepo.StatusEnabled,
			Condition:  "valueNumber > 10",
		}

		assetDid := randAssetDID(t)
		testData := map[string]map[string][]*Webhook{
			assetDid.String(): {
				"speed": []*Webhook{
					{
						Trigger: trigger1,
						Program: nil,
					},
					{
						Trigger: trigger2,
						Program: nil,
					},
				},
			},
		}

		cache.Update(testData)

		// Test
		webhooks := cache.GetWebhooks(assetDid.String(), "speed")

		// Verify
		require.Len(t, webhooks, 2)
		triggerIDs := []string{webhooks[0].Trigger.ID, webhooks[1].Trigger.ID}
		assert.Contains(t, triggerIDs, "trigger-1")
		assert.Contains(t, triggerIDs, "trigger-2")
	})
}

func TestWebhookCache_Update(t *testing.T) {
	t.Parallel()

	t.Run("updates cache with new data", func(t *testing.T) {
		ctrl := gomock.NewController(t)
		defer ctrl.Finish()

		mockRepo := NewMockRepository(ctrl)
		cache := NewWebhookCache(mockRepo, &config.Settings{})

		// Initial data
		trigger1 := &models.Trigger{
			ID:         "trigger-1",
			MetricName: "speed",
			Status:     triggersrepo.StatusEnabled,
			Condition:  "valueNumber > 10",
		}

		assetDid1 := randAssetDID(t)
		initialData := map[string]map[string][]*Webhook{
			assetDid1.String(): {
				"speed": []*Webhook{
					{
						Trigger: trigger1,
						Program: nil,
					},
				},
			},
		}

		cache.Update(initialData)

		// Verify initial state
		webhooks := cache.GetWebhooks(assetDid1.String(), "speed")
		require.Len(t, webhooks, 1)
		assert.Equal(t, "trigger-1", webhooks[0].Trigger.ID)

		// New data
		trigger2 := &models.Trigger{
			ID:         "trigger-2",
			MetricName: "temperature",
			Status:     triggersrepo.StatusEnabled,
			Condition:  "valueNumber > 10",
		}

		assetDid2 := randAssetDID(t)
		newData := map[string]map[string][]*Webhook{
			assetDid2.String(): {
				"temperature": []*Webhook{
					{
						Trigger: trigger2,
						Program: nil,
					},
				},
			},
		}

		cache.Update(newData)

		// Verify old data is gone and new data is present
		webhooks = cache.GetWebhooks(assetDid1.String(), "speed")
		assert.Nil(t, webhooks)

		webhooks = cache.GetWebhooks(assetDid2.String(), "temperature")
		require.Len(t, webhooks, 1)
		assert.Equal(t, "trigger-2", webhooks[0].Trigger.ID)
	})

	t.Run("handles empty update", func(t *testing.T) {
		ctrl := gomock.NewController(t)
		defer ctrl.Finish()

		mockRepo := NewMockRepository(ctrl)
		cache := NewWebhookCache(mockRepo, &config.Settings{})

		// Initial data
		trigger := &models.Trigger{
			ID:         "trigger-1",
			MetricName: "speed",
			Status:     triggersrepo.StatusEnabled,
			Condition:  "valueNumber > 10",
		}

		assetDid := randAssetDID(t)
		initialData := map[string]map[string][]*Webhook{
			assetDid.String(): {
				"speed": []*Webhook{
					{
						Trigger: trigger,
						Program: nil,
					},
				},
			},
		}

		cache.Update(initialData)

		// Update with empty data
		cache.Update(map[string]map[string][]*Webhook{})

		// Verify cache is now empty
		webhooks := cache.GetWebhooks(assetDid.String(), "speed")
		assert.Nil(t, webhooks)
	})
}

func randAssetDID(t *testing.T) cloudevent.ERC721DID {
	tokenID := make([]byte, 32)
	_, err := rand.Read(tokenID)
	if err != nil {
		t.Fatalf("couldn't create a test token ID: %v", err)
	}
	return cloudevent.ERC721DID{
		ChainID:         137,
		ContractAddress: common.HexToAddress("0xbA5738a18d83D41847dfFbDC6101d37C69c9B0cF"),
		TokenID:         new(big.Int).SetBytes(tokenID),
	}
}
