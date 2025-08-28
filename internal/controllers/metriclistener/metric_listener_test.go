//go:generate go tool mockgen -source=metric_listener.go -destination=metric_listener_mock_test.go -package=metriclistener

package metriclistener

import (
	"context"
	"encoding/json"
	"math/big"
	"testing"
	"time"

	"github.com/DIMO-Network/cloudevent"
	"github.com/DIMO-Network/model-garage/pkg/vss"
	"github.com/DIMO-Network/vehicle-triggers-api/internal/config"
	"github.com/DIMO-Network/vehicle-triggers-api/internal/db/models"
	"github.com/DIMO-Network/vehicle-triggers-api/internal/services/triggerevaluator"
	"github.com/DIMO-Network/vehicle-triggers-api/internal/services/triggersrepo"
	"github.com/DIMO-Network/vehicle-triggers-api/internal/services/webhookcache"

	"github.com/ThreeDotsLabs/watermill/message"
	"github.com/ethereum/go-ethereum/common"
	"github.com/google/uuid"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"go.uber.org/mock/gomock"
)

func TestNewMetricsListener(t *testing.T) {
	t.Parallel()

	t.Run("creates listener with correct settings", func(t *testing.T) {
		ctrl := gomock.NewController(t)
		defer ctrl.Finish()

		mockCache := NewMockWebhookCache(ctrl)
		mockRepo := NewMockTriggerRepo(ctrl)
		mockWebhookSender := NewMockWebhookSender(ctrl)
		mockTriggerEvaluator := NewMockTriggerEvaluator(ctrl)
		settings := createTestSettings()

		listener := NewMetricsListener(mockCache, mockRepo, mockWebhookSender, mockTriggerEvaluator, settings)

		require.NotNil(t, listener)
		assert.Equal(t, settings.VehicleNFTAddress, listener.vehicleNFTAddress)
		assert.Equal(t, settings.DIMORegistryChainID, listener.dimoRegistryChainID)
		assert.Equal(t, int(settings.MaxWebhookFailureCount), listener.maxFailureCount)
	})
}

func TestMetricListener_ShouldAttemptWebhook(t *testing.T) {
	t.Parallel()

	listener := &MetricListener{maxFailureCount: 5}

	tests := []struct {
		name         string
		status       string
		failureCount int
		expected     bool
	}{
		{
			name:         "should attempt when enabled and under threshold",
			status:       triggersrepo.StatusEnabled,
			failureCount: 2,
			expected:     true,
		},
		{
			name:         "should not attempt when disabled",
			status:       triggersrepo.StatusDisabled,
			failureCount: 2,
			expected:     false,
		},
		{
			name:         "should not attempt when failed",
			status:       triggersrepo.StatusFailed,
			failureCount: 2,
			expected:     false,
		},
		{
			name:         "should not attempt when at failure threshold",
			status:       triggersrepo.StatusEnabled,
			failureCount: 5,
			expected:     false,
		},
		{
			name:         "should not attempt when beyond failure threshold",
			status:       triggersrepo.StatusEnabled,
			failureCount: 6,
			expected:     false,
		},
		{
			name:         "should attempt when exactly under threshold",
			status:       triggersrepo.StatusEnabled,
			failureCount: 4,
			expected:     true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			trigger := &models.Trigger{
				Status:       tt.status,
				FailureCount: tt.failureCount,
			}
			result := listener.ShouldAttemptWebhook(trigger)
			assert.Equal(t, tt.expected, result)
		})
	}
}

func TestMetricListener_ProcessSignalMessages(t *testing.T) {
	t.Parallel()

	t.Run("processes messages until channel is closed", func(t *testing.T) {
		ctrl := gomock.NewController(t)
		defer ctrl.Finish()

		mockCache := NewMockWebhookCache(ctrl)
		mockRepo := NewMockTriggerRepo(ctrl)
		mockWebhookSender := NewMockWebhookSender(ctrl)
		mockTriggerEvaluator := NewMockTriggerEvaluator(ctrl)

		listener := NewMetricsListener(mockCache, mockRepo, mockWebhookSender, mockTriggerEvaluator, createTestSettings())
		ctx := context.Background()

		// Create proper VSS signal data
		testSignal := vss.Signal{
			TokenID:     12345,
			Timestamp:   time.Now().UTC(),
			Name:        "speed",
			ValueNumber: 25.0,
			ValueString: "",
			Source:      "test-source",
			Producer:    "test-producer",
		}

		signalJSON, err := json.Marshal(testSignal)
		require.NoError(t, err)

		// Mock expectations - no webhooks found for this signal
		vehicleDID := cloudevent.ERC721DID{
			ChainID:         137,
			ContractAddress: common.HexToAddress("0xbA5738a18d83D41847dfFbDC6101d37C69c9B0cF"),
			TokenID:         big.NewInt(12345),
		}

		mockCache.EXPECT().
			GetWebhooks(vehicleDID.String(), triggersrepo.ServiceSignal, "speed").
			Return([]*webhookcache.Webhook{}).
			Times(1)

		messages := make(chan *message.Message, 1)
		msg := message.NewMessage(uuid.New().String(), signalJSON)
		messages <- msg
		close(messages)

		err = listener.ProcessSignalMessages(ctx, messages)
		require.NoError(t, err)
	})

	t.Run("processes signal with webhook trigger", func(t *testing.T) {
		ctrl := gomock.NewController(t)
		defer ctrl.Finish()

		mockCache := NewMockWebhookCache(ctrl)
		mockRepo := NewMockTriggerRepo(ctrl)
		mockWebhookSender := NewMockWebhookSender(ctrl)
		mockTriggerEvaluator := NewMockTriggerEvaluator(ctrl)

		listener := NewMetricsListener(mockCache, mockRepo, mockWebhookSender, mockTriggerEvaluator, createTestSettings())
		ctx := context.Background()

		// Create proper VSS signal data
		testSignal := vss.Signal{
			TokenID:     12345,
			Timestamp:   time.Now().UTC(),
			Name:        "speed",
			ValueNumber: 25.0,
			ValueString: "",
			Source:      "test-source",
			Producer:    "test-producer",
		}

		signalJSON, err := json.Marshal(testSignal)
		require.NoError(t, err)

		vehicleDID := cloudevent.ERC721DID{
			ChainID:         137,
			ContractAddress: common.HexToAddress("0xbA5738a18d83D41847dfFbDC6101d37C69c9B0cF"),
			TokenID:         big.NewInt(12345),
		}

		// Create mock webhook and trigger
		mockTrigger := &models.Trigger{
			ID:           "test-trigger-id",
			Status:       triggersrepo.StatusEnabled,
			Service:      triggersrepo.ServiceSignal,
			MetricName:   "speed",
			Condition:    "valueNumber > 20",
			DisplayName:  "Speed Alert",
			FailureCount: 0,
		}

		mockWebhook := &webhookcache.Webhook{
			Trigger: mockTrigger,
		}

		// Mock expectations
		mockCache.EXPECT().
			GetWebhooks(vehicleDID.String(), triggersrepo.ServiceSignal, "speed").
			Return([]*webhookcache.Webhook{mockWebhook}).
			Times(1)

		mockTriggerEvaluator.EXPECT().
			EvaluateSignalTrigger(gomock.Any(), mockTrigger, gomock.Any(), gomock.Any()).
			Return(&triggerevaluator.TriggerEvaluationResult{
				ShouldFire:       true,
				PermissionDenied: false,
			}, nil).
			Times(1)
		mockWebhookSender.EXPECT().
			SendWebhook(gomock.Any(), mockTrigger, gomock.Any()).
			Return(nil).
			Times(1)

		mockRepo.EXPECT().
			CreateTriggerLog(gomock.Any(), gomock.Any()).
			Return(nil).
			Times(1)

		messages := make(chan *message.Message, 1)
		msg := message.NewMessage(uuid.New().String(), signalJSON)
		messages <- msg
		close(messages)

		err = listener.ProcessSignalMessages(ctx, messages)
		require.NoError(t, err)
	})

	t.Run("stops processing when context is cancelled", func(t *testing.T) {
		ctrl := gomock.NewController(t)
		defer ctrl.Finish()

		mockCache := NewMockWebhookCache(ctrl)
		mockRepo := NewMockTriggerRepo(ctrl)
		mockWebhookSender := NewMockWebhookSender(ctrl)
		mockTriggerEvaluator := NewMockTriggerEvaluator(ctrl)

		listener := NewMetricsListener(mockCache, mockRepo, mockWebhookSender, mockTriggerEvaluator, createTestSettings())
		ctx, cancel := context.WithCancel(context.Background())
		cancel() // Cancel immediately

		messages := make(chan *message.Message, 1)
		testSignal := vss.Signal{
			TokenID:     12345,
			Timestamp:   time.Now().UTC(),
			Name:        "speed",
			ValueNumber: 25.0,
		}
		signalJSON, err := json.Marshal(testSignal)
		require.NoError(t, err)

		msg := message.NewMessage(uuid.New().String(), signalJSON)
		messages <- msg

		err = listener.ProcessSignalMessages(ctx, messages)
		require.Error(t, err)
		assert.Equal(t, context.Canceled, err)
	})
}

func TestMetricListener_ProcessEventMessages(t *testing.T) {
	t.Parallel()

	t.Run("processes messages until channel is closed", func(t *testing.T) {
		ctrl := gomock.NewController(t)
		defer ctrl.Finish()

		mockCache := NewMockWebhookCache(ctrl)
		mockRepo := NewMockTriggerRepo(ctrl)
		mockWebhookSender := NewMockWebhookSender(ctrl)
		mockTriggerEvaluator := NewMockTriggerEvaluator(ctrl)

		listener := NewMetricsListener(mockCache, mockRepo, mockWebhookSender, mockTriggerEvaluator, createTestSettings())
		ctx := context.Background()

		// Create proper VSS event data
		vehicleDID := cloudevent.ERC721DID{
			ChainID:         137,
			ContractAddress: common.HexToAddress("0xbA5738a18d83D41847dfFbDC6101d37C69c9B0cF"),
			TokenID:         big.NewInt(12345),
		}
		testEvent := vss.Event{
			Subject:    vehicleDID.String(),
			Timestamp:  time.Now().UTC(),
			Name:       "HarshBraking",
			DurationNs: 1000000, // 1ms
		}

		eventJSON, err := json.Marshal([]vss.Event{testEvent})
		require.NoError(t, err)

		// Mock expectations - no webhooks found for this event

		mockCache.EXPECT().
			GetWebhooks(vehicleDID.String(), triggersrepo.ServiceEvent, "HarshBraking").
			Return([]*webhookcache.Webhook{}).
			Times(1)

		messages := make(chan *message.Message, 1)
		msg := message.NewMessage(uuid.New().String(), eventJSON)
		messages <- msg
		close(messages)

		err = listener.ProcessEventMessages(ctx, messages)
		require.NoError(t, err)
	})

	t.Run("processes event with webhook trigger", func(t *testing.T) {
		ctrl := gomock.NewController(t)
		defer ctrl.Finish()

		mockCache := NewMockWebhookCache(ctrl)
		mockRepo := NewMockTriggerRepo(ctrl)
		mockWebhookSender := NewMockWebhookSender(ctrl)
		mockTriggerEvaluator := NewMockTriggerEvaluator(ctrl)

		listener := NewMetricsListener(mockCache, mockRepo, mockWebhookSender, mockTriggerEvaluator, createTestSettings())
		ctx := context.Background()

		// Create proper VSS event data
		vehicleDID := cloudevent.ERC721DID{
			ChainID:         137,
			ContractAddress: common.HexToAddress("0xbA5738a18d83D41847dfFbDC6101d37C69c9B0cF"),
			TokenID:         big.NewInt(12345),
		}
		testEvent := vss.Event{
			Subject:    vehicleDID.String(),
			Timestamp:  time.Now().UTC(),
			Name:       "HarshBraking",
			DurationNs: 2000000, // 2ms - above threshold
		}

		eventJSON, err := json.Marshal([]vss.Event{testEvent})
		require.NoError(t, err)

		// Create mock webhook and trigger
		mockTrigger := &models.Trigger{
			ID:           "test-event-trigger-id",
			Status:       triggersrepo.StatusEnabled,
			Service:      triggersrepo.ServiceEvent,
			MetricName:   "HarshBraking",
			Condition:    "durationNs > 1000000",
			DisplayName:  "Harsh Braking Alert",
			FailureCount: 0,
		}

		mockWebhook := &webhookcache.Webhook{
			Trigger: mockTrigger,
		}

		// Mock expectations
		mockCache.EXPECT().
			GetWebhooks(vehicleDID.String(), triggersrepo.ServiceEvent, "HarshBraking").
			Return([]*webhookcache.Webhook{mockWebhook}).
			Times(1)

		mockTriggerEvaluator.EXPECT().
			EvaluateEventTrigger(gomock.Any(), mockTrigger, gomock.Any(), gomock.Any()).
			Return(&triggerevaluator.TriggerEvaluationResult{
				ShouldFire:       true,
				PermissionDenied: false,
			}, nil).
			Times(1)

		mockWebhookSender.EXPECT().
			SendWebhook(gomock.Any(), mockTrigger, gomock.Any()).
			Return(nil).
			Times(1)

		mockRepo.EXPECT().
			CreateTriggerLog(gomock.Any(), gomock.Any()).
			Return(nil).
			Times(1)

		messages := make(chan *message.Message, 1)
		msg := message.NewMessage(uuid.New().String(), eventJSON)
		messages <- msg
		close(messages)

		err = listener.ProcessEventMessages(ctx, messages)
		require.NoError(t, err)
	})

	t.Run("stops processing when context is cancelled", func(t *testing.T) {
		ctrl := gomock.NewController(t)
		defer ctrl.Finish()

		mockCache := NewMockWebhookCache(ctrl)
		mockRepo := NewMockTriggerRepo(ctrl)
		mockWebhookSender := NewMockWebhookSender(ctrl)
		mockTriggerEvaluator := NewMockTriggerEvaluator(ctrl)

		listener := NewMetricsListener(mockCache, mockRepo, mockWebhookSender, mockTriggerEvaluator, createTestSettings())
		ctx, cancel := context.WithCancel(context.Background())
		cancel() // Cancel immediately

		messages := make(chan *message.Message, 1)
		vehicleDID := cloudevent.ERC721DID{
			ChainID:         137,
			ContractAddress: common.HexToAddress("0xbA5738a18d83D41847dfFbDC6101d37C69c9B0cF"),
			TokenID:         big.NewInt(12345),
		}
		testEvent := vss.Event{
			Subject:    vehicleDID.String(),
			Timestamp:  time.Now().UTC(),
			Name:       "HarshBraking",
			DurationNs: 1000000,
		}
		eventJSON, err := json.Marshal([]vss.Event{testEvent})
		require.NoError(t, err)

		msg := message.NewMessage(uuid.New().String(), eventJSON)
		messages <- msg

		err = listener.ProcessEventMessages(ctx, messages)
		require.Error(t, err)
		assert.Equal(t, context.Canceled, err)
	})
}

// Helper functions for creating test data
func createTestSettings() *config.Settings {
	return &config.Settings{
		VehicleNFTAddress:      common.HexToAddress("0xbA5738a18d83D41847dfFbDC6101d37C69c9B0cF"),
		DIMORegistryChainID:    137,
		MaxWebhookFailureCount: 5,
	}
}
