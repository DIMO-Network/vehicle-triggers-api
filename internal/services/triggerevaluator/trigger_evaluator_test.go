//go:generate go tool mockgen -source=trigger_evaluator.go -destination=trigger_evaluator_mock_test.go -package=triggerevaluator

package triggerevaluator

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"math/big"
	"net/http"
	"testing"
	"time"

	"github.com/DIMO-Network/cloudevent"
	"github.com/DIMO-Network/model-garage/pkg/vss"
	"github.com/DIMO-Network/server-garage/pkg/richerrors"
	"github.com/DIMO-Network/vehicle-triggers-api/internal/celcondition"
	"github.com/DIMO-Network/vehicle-triggers-api/internal/db/models"
	"github.com/DIMO-Network/vehicle-triggers-api/internal/signals"
	"github.com/ethereum/go-ethereum/common"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"go.uber.org/mock/gomock"
)

func TestNewTriggerEvaluator(t *testing.T) {
	t.Parallel()

	t.Run("creates evaluator with dependencies", func(t *testing.T) {
		ctrl := gomock.NewController(t)

		mockRepo := NewMockTriggerRepo(ctrl)
		mockTokenClient := NewMockTokenExchangeClient(ctrl)

		evaluator := NewTriggerEvaluator(mockRepo, mockTokenClient)

		require.NotNil(t, evaluator)
		assert.Equal(t, mockRepo, evaluator.repo)
		assert.Equal(t, mockTokenClient, evaluator.tokenClient)
	})
}

func TestTriggerEvaluator_EvaluateSignalTrigger(t *testing.T) {
	t.Parallel()

	t.Run("successful evaluation - should fire", func(t *testing.T) {
		ctrl := gomock.NewController(t)

		mockRepo := NewMockTriggerRepo(ctrl)
		mockTokenClient := NewMockTokenExchangeClient(ctrl)
		evaluator := NewTriggerEvaluator(mockRepo, mockTokenClient)

		ctx := context.Background()
		trigger := createTestTrigger()
		signalData := createTestSignalData()
		program, err := celcondition.PrepareSignalCondition("value > previousValue", signalData.Def.ValueType)
		require.NoError(t, err)

		// Mock permission check - success
		mockTokenClient.EXPECT().
			HasVehiclePermissions(ctx, signalData.VehicleDID, common.BytesToAddress(trigger.DeveloperLicenseAddress), signalData.Def.Permissions).
			Return(true, nil).
			Times(1)

		// Mock last log retrieval
		lastLog := &models.TriggerLog{
			SnapshotData: snapShotFromSignal(t, vss.Signal{
				Timestamp:   signalData.Signal.Timestamp.Add(-time.Hour), // previous value 1 hour ago
				ValueNumber: 59,
			}),
			AssetDid:        signalData.VehicleDID.String(),
			TriggerID:       trigger.ID,
			LastTriggeredAt: time.Now().Add(-time.Hour), // Cooldown passed
		}
		mockRepo.EXPECT().
			GetLastLogValue(ctx, trigger.ID, signalData.VehicleDID).
			Return(lastLog, nil).
			Times(1)

		result, err := evaluator.EvaluateSignalTrigger(ctx, trigger, program, signalData)

		require.NoError(t, err)
		require.NotNil(t, result)
		assert.True(t, result.ShouldFire)
		assert.False(t, result.PermissionDenied)
		assert.False(t, result.CoolDownNotMet)
		assert.False(t, result.ConditionNotMet)
	})

	t.Run("permission denied", func(t *testing.T) {
		ctrl := gomock.NewController(t)
		defer ctrl.Finish()

		mockRepo := NewMockTriggerRepo(ctrl)
		mockTokenClient := NewMockTokenExchangeClient(ctrl)
		evaluator := NewTriggerEvaluator(mockRepo, mockTokenClient)

		ctx := context.Background()
		trigger := createTestTrigger()
		signalData := createTestSignalData()
		program, err := celcondition.PrepareSignalCondition("value > previousValue", signalData.Def.ValueType)
		require.NoError(t, err)

		// Mock permission check - denied
		mockTokenClient.EXPECT().
			HasVehiclePermissions(ctx, signalData.VehicleDID, common.BytesToAddress(trigger.DeveloperLicenseAddress), signalData.Def.Permissions).
			Return(false, nil).
			Times(1)

		result, err := evaluator.EvaluateSignalTrigger(ctx, trigger, program, signalData)

		require.NoError(t, err)
		require.NotNil(t, result)
		assert.False(t, result.ShouldFire)
		assert.True(t, result.PermissionDenied)
		assert.False(t, result.CoolDownNotMet)
		assert.False(t, result.ConditionNotMet)
	})

	t.Run("permission check error", func(t *testing.T) {
		ctrl := gomock.NewController(t)
		defer ctrl.Finish()

		mockRepo := NewMockTriggerRepo(ctrl)
		mockTokenClient := NewMockTokenExchangeClient(ctrl)
		evaluator := NewTriggerEvaluator(mockRepo, mockTokenClient)

		ctx := context.Background()
		trigger := createTestTrigger()
		signalData := createTestSignalData()
		program, err := celcondition.PrepareSignalCondition("value > previousValue", signalData.Def.ValueType)
		require.NoError(t, err)

		// Mock permission check - error
		mockTokenClient.EXPECT().
			HasVehiclePermissions(ctx, signalData.VehicleDID, common.BytesToAddress(trigger.DeveloperLicenseAddress), signalData.Def.Permissions).
			Return(false, errors.New("permission service error")).
			Times(1)

		result, err := evaluator.EvaluateSignalTrigger(ctx, trigger, program, signalData)

		require.Error(t, err)
		assert.Nil(t, result)
		richErr, ok := richerrors.AsRichError(err)
		require.True(t, ok)
		assert.Equal(t, http.StatusInternalServerError, richErr.Code)
		assert.Contains(t, richErr.ExternalMsg, "failed to check permissions for signal trigger")
	})

	t.Run("cooldown not met", func(t *testing.T) {
		ctrl := gomock.NewController(t)
		defer ctrl.Finish()

		mockRepo := NewMockTriggerRepo(ctrl)
		mockTokenClient := NewMockTokenExchangeClient(ctrl)
		evaluator := NewTriggerEvaluator(mockRepo, mockTokenClient)

		ctx := context.Background()
		trigger := createTestTrigger()
		trigger.CooldownPeriod = int(time.Hour.Seconds()) // 1 hour cooldown
		signalData := createTestSignalData()
		program, err := celcondition.PrepareSignalCondition("value > previousValue", signalData.Def.ValueType)
		require.NoError(t, err)

		// Mock permission check - success
		mockTokenClient.EXPECT().
			HasVehiclePermissions(ctx, signalData.VehicleDID, common.BytesToAddress(trigger.DeveloperLicenseAddress), signalData.Def.Permissions).
			Return(true, nil).
			Times(1)

		// Mock last log retrieval - recent trigger
		lastLog := &models.TriggerLog{
			SnapshotData: snapShotFromSignal(t, vss.Signal{
				Timestamp:   signalData.Signal.Timestamp.Add(-time.Hour), // previous value 1 hour ago
				ValueNumber: 59,
			}),
			AssetDid:        signalData.VehicleDID.String(),
			TriggerID:       trigger.ID,
			LastTriggeredAt: time.Now().Add(-30 * time.Minute), // Cooldown not passed
		}
		mockRepo.EXPECT().
			GetLastLogValue(ctx, trigger.ID, signalData.VehicleDID).
			Return(lastLog, nil).
			Times(1)

		result, err := evaluator.EvaluateSignalTrigger(ctx, trigger, program, signalData)

		require.NoError(t, err)
		require.NotNil(t, result)
		assert.False(t, result.ShouldFire)
		assert.False(t, result.PermissionDenied)
		assert.True(t, result.CoolDownNotMet)
		assert.False(t, result.ConditionNotMet)
	})

	t.Run("condition not met", func(t *testing.T) {
		ctrl := gomock.NewController(t)
		defer ctrl.Finish()

		mockRepo := NewMockTriggerRepo(ctrl)
		mockTokenClient := NewMockTokenExchangeClient(ctrl)
		evaluator := NewTriggerEvaluator(mockRepo, mockTokenClient)

		ctx := context.Background()
		trigger := createTestTrigger()
		signalData := createTestSignalData()
		program, err := celcondition.PrepareSignalCondition("value > previousValue", signalData.Def.ValueType)
		require.NoError(t, err)

		// Mock permission check - success
		mockTokenClient.EXPECT().
			HasVehiclePermissions(ctx, signalData.VehicleDID, common.BytesToAddress(trigger.DeveloperLicenseAddress), signalData.Def.Permissions).
			Return(true, nil).
			Times(1)

		// Mock last log retrieval
		lastLog := &models.TriggerLog{
			SnapshotData: snapShotFromSignal(t, vss.Signal{
				Timestamp:   signalData.Signal.Timestamp.Add(-time.Hour),
				ValueNumber: 60, // value the same as current
			}),
			AssetDid:        signalData.VehicleDID.String(),
			TriggerID:       trigger.ID,
			LastTriggeredAt: time.Now().Add(-time.Hour), // Cooldown passed
		}
		mockRepo.EXPECT().
			GetLastLogValue(ctx, trigger.ID, signalData.VehicleDID).
			Return(lastLog, nil).
			Times(1)

		result, err := evaluator.EvaluateSignalTrigger(ctx, trigger, program, signalData)

		require.NoError(t, err)
		require.NotNil(t, result)
		assert.False(t, result.ShouldFire)
		assert.False(t, result.PermissionDenied)
		assert.False(t, result.CoolDownNotMet)
		assert.True(t, result.ConditionNotMet)
	})

	t.Run("no previous log - first trigger", func(t *testing.T) {
		ctrl := gomock.NewController(t)
		defer ctrl.Finish()

		mockRepo := NewMockTriggerRepo(ctrl)
		mockTokenClient := NewMockTokenExchangeClient(ctrl)
		evaluator := NewTriggerEvaluator(mockRepo, mockTokenClient)

		ctx := context.Background()
		trigger := createTestTrigger()
		signalData := createTestSignalData()
		program, err := celcondition.PrepareSignalCondition("value > previousValue", signalData.Def.ValueType)
		require.NoError(t, err)

		// Mock permission check - success
		mockTokenClient.EXPECT().
			HasVehiclePermissions(ctx, signalData.VehicleDID, common.BytesToAddress(trigger.DeveloperLicenseAddress), signalData.Def.Permissions).
			Return(true, nil).
			Times(1)

		// Mock last log retrieval - no rows (first trigger)
		mockRepo.EXPECT().
			GetLastLogValue(ctx, trigger.ID, signalData.VehicleDID).
			Return(nil, sql.ErrNoRows).
			Times(1)

		result, err := evaluator.EvaluateSignalTrigger(ctx, trigger, program, signalData)

		require.NoError(t, err)
		require.NotNil(t, result)
		assert.True(t, result.ShouldFire)
		assert.False(t, result.PermissionDenied)
		assert.False(t, result.CoolDownNotMet)
		assert.False(t, result.ConditionNotMet)
	})

	t.Run("database error", func(t *testing.T) {
		ctrl := gomock.NewController(t)
		defer ctrl.Finish()

		mockRepo := NewMockTriggerRepo(ctrl)
		mockTokenClient := NewMockTokenExchangeClient(ctrl)
		evaluator := NewTriggerEvaluator(mockRepo, mockTokenClient)

		ctx := context.Background()
		trigger := createTestTrigger()
		signalData := createTestSignalData()
		program, err := celcondition.PrepareSignalCondition("value > previousValue", signalData.Def.ValueType)
		require.NoError(t, err)

		// Mock permission check - success
		mockTokenClient.EXPECT().
			HasVehiclePermissions(ctx, signalData.VehicleDID, common.BytesToAddress(trigger.DeveloperLicenseAddress), signalData.Def.Permissions).
			Return(true, nil).
			Times(1)

		// Mock last log retrieval - database error
		mockRepo.EXPECT().
			GetLastLogValue(ctx, trigger.ID, signalData.VehicleDID).
			Return(nil, errors.New("database connection error")).
			Times(1)

		result, err := evaluator.EvaluateSignalTrigger(ctx, trigger, program, signalData)

		require.Error(t, err)
		assert.Nil(t, result)
		richErr, ok := richerrors.AsRichError(err)
		require.True(t, ok)
		assert.Equal(t, http.StatusInternalServerError, richErr.Code)
	})

	t.Run("invalid previous signal JSON", func(t *testing.T) {
		ctrl := gomock.NewController(t)
		defer ctrl.Finish()

		mockRepo := NewMockTriggerRepo(ctrl)
		mockTokenClient := NewMockTokenExchangeClient(ctrl)
		evaluator := NewTriggerEvaluator(mockRepo, mockTokenClient)

		ctx := context.Background()
		trigger := createTestTrigger()
		signalData := createTestSignalData()
		program, err := celcondition.PrepareSignalCondition("value > previousValue", signalData.Def.ValueType)
		require.NoError(t, err)

		// Mock permission check - success
		mockTokenClient.EXPECT().
			HasVehiclePermissions(ctx, signalData.VehicleDID, common.BytesToAddress(trigger.DeveloperLicenseAddress), signalData.Def.Permissions).
			Return(true, nil).
			Times(1)

		// Mock last log retrieval - invalid JSON
		lastLog := &models.TriggerLog{
			SnapshotData:    []byte(`invalid json`),
			AssetDid:        signalData.VehicleDID.String(),
			TriggerID:       trigger.ID,
			LastTriggeredAt: time.Now().Add(-time.Hour),
		}
		mockRepo.EXPECT().
			GetLastLogValue(ctx, trigger.ID, signalData.VehicleDID).
			Return(lastLog, nil).
			Times(1)

		result, err := evaluator.EvaluateSignalTrigger(ctx, trigger, program, signalData)

		require.Error(t, err)
		assert.Nil(t, result)
		richErr, ok := richerrors.AsRichError(err)
		require.True(t, ok)
		assert.Equal(t, http.StatusInternalServerError, richErr.Code)
	})
}

func TestTriggerEvaluator_EvaluateEventTrigger(t *testing.T) {
	t.Parallel()

	t.Run("successful evaluation - should fire", func(t *testing.T) {
		ctrl := gomock.NewController(t)
		defer ctrl.Finish()

		mockRepo := NewMockTriggerRepo(ctrl)
		mockTokenClient := NewMockTokenExchangeClient(ctrl)
		evaluator := NewTriggerEvaluator(mockRepo, mockTokenClient)

		ctx := context.Background()
		trigger := createTestTrigger()
		eventData := createTestEventData()
		program, err := celcondition.PrepareEventCondition("durationNs > previousDurationNs")
		require.NoError(t, err)

		// Mock permission check - success
		expectedPermissions := []string{
			"privilege:GetNonLocationHistory",
			"privilege:GetLocationHistory",
		}
		mockTokenClient.EXPECT().
			HasVehiclePermissions(ctx, eventData.VehicleDID, common.BytesToAddress(trigger.DeveloperLicenseAddress), expectedPermissions).
			Return(true, nil).
			Times(1)

		// Mock last log retrieval
		lastLog := &models.TriggerLog{
			SnapshotData: snapShotFromEvent(t, vss.Event{
				Timestamp:  eventData.Event.Timestamp.Add(-time.Hour),
				DurationNs: 5,
				Name:       "ignition",
			}),
			AssetDid:        eventData.VehicleDID.String(),
			TriggerID:       trigger.ID,
			LastTriggeredAt: time.Now().Add(-time.Hour), // Cooldown passed
		}
		mockRepo.EXPECT().
			GetLastLogValue(ctx, trigger.ID, eventData.VehicleDID).
			Return(lastLog, nil).
			Times(1)

		result, err := evaluator.EvaluateEventTrigger(ctx, trigger, program, eventData)

		require.NoError(t, err)
		require.NotNil(t, result)
		assert.True(t, result.ShouldFire)
		assert.False(t, result.PermissionDenied)
		assert.False(t, result.CoolDownNotMet)
		assert.False(t, result.ConditionNotMet)
	})

	t.Run("permission denied", func(t *testing.T) {
		ctrl := gomock.NewController(t)
		defer ctrl.Finish()

		mockRepo := NewMockTriggerRepo(ctrl)
		mockTokenClient := NewMockTokenExchangeClient(ctrl)
		evaluator := NewTriggerEvaluator(mockRepo, mockTokenClient)

		ctx := context.Background()
		trigger := createTestTrigger()
		eventData := createTestEventData()
		program, err := celcondition.PrepareEventCondition("durationNs > previousDurationNs")
		require.NoError(t, err)

		// Mock permission check - denied
		expectedPermissions := []string{
			"privilege:GetNonLocationHistory",
			"privilege:GetLocationHistory",
		}
		mockTokenClient.EXPECT().
			HasVehiclePermissions(ctx, eventData.VehicleDID, common.BytesToAddress(trigger.DeveloperLicenseAddress), expectedPermissions).
			Return(false, nil).
			Times(1)

		result, err := evaluator.EvaluateEventTrigger(ctx, trigger, program, eventData)

		require.NoError(t, err)
		require.NotNil(t, result)
		assert.False(t, result.ShouldFire)
		assert.True(t, result.PermissionDenied)
		assert.False(t, result.CoolDownNotMet)
		assert.False(t, result.ConditionNotMet)
	})

	t.Run("cooldown not met", func(t *testing.T) {
		ctrl := gomock.NewController(t)
		defer ctrl.Finish()

		mockRepo := NewMockTriggerRepo(ctrl)
		mockTokenClient := NewMockTokenExchangeClient(ctrl)
		evaluator := NewTriggerEvaluator(mockRepo, mockTokenClient)

		ctx := context.Background()
		trigger := createTestTrigger()
		trigger.CooldownPeriod = 3600 // 1 hour cooldown
		eventData := createTestEventData()
		program, err := celcondition.PrepareEventCondition("durationNs > previousDurationNs")
		require.NoError(t, err)

		// Mock permission check - success
		expectedPermissions := []string{
			"privilege:GetNonLocationHistory",
			"privilege:GetLocationHistory",
		}
		mockTokenClient.EXPECT().
			HasVehiclePermissions(ctx, eventData.VehicleDID, common.BytesToAddress(trigger.DeveloperLicenseAddress), expectedPermissions).
			Return(true, nil).
			Times(1)

		// Mock last log retrieval - recent trigger
		lastLog := &models.TriggerLog{
			SnapshotData: snapShotFromEvent(t, vss.Event{
				Timestamp:  eventData.Event.Timestamp.Add(-time.Hour),
				DurationNs: 5,
				Name:       "HarshBraking",
			}),
			AssetDid:        eventData.VehicleDID.String(),
			TriggerID:       trigger.ID,
			LastTriggeredAt: time.Now().Add(-30 * time.Minute), // Cooldown not passed
		}
		mockRepo.EXPECT().
			GetLastLogValue(ctx, trigger.ID, eventData.VehicleDID).
			Return(lastLog, nil).
			Times(1)

		result, err := evaluator.EvaluateEventTrigger(ctx, trigger, program, eventData)

		require.NoError(t, err)
		require.NotNil(t, result)
		assert.False(t, result.ShouldFire)
		assert.False(t, result.PermissionDenied)
		assert.True(t, result.CoolDownNotMet)
		assert.False(t, result.ConditionNotMet)
	})

	t.Run("condition not met", func(t *testing.T) {
		ctrl := gomock.NewController(t)
		defer ctrl.Finish()

		mockRepo := NewMockTriggerRepo(ctrl)
		mockTokenClient := NewMockTokenExchangeClient(ctrl)
		evaluator := NewTriggerEvaluator(mockRepo, mockTokenClient)

		ctx := context.Background()
		trigger := createTestTrigger()
		eventData := createTestEventData()
		program, err := celcondition.PrepareEventCondition("durationNs > previousDurationNs")
		require.NoError(t, err)

		// Mock permission check - success
		expectedPermissions := []string{
			"privilege:GetNonLocationHistory",
			"privilege:GetLocationHistory",
		}
		mockTokenClient.EXPECT().
			HasVehiclePermissions(ctx, eventData.VehicleDID, common.BytesToAddress(trigger.DeveloperLicenseAddress), expectedPermissions).
			Return(true, nil).
			Times(1)

		// Mock last log retrieval - same event type
		lastLog := &models.TriggerLog{
			SnapshotData: snapShotFromEvent(t, vss.Event{
				Timestamp:  eventData.Event.Timestamp.Add(-time.Hour),
				DurationNs: 15,
				Name:       "HarshBraking",
			}), // Same as current
			AssetDid:        eventData.VehicleDID.String(),
			TriggerID:       trigger.ID,
			LastTriggeredAt: time.Now().Add(-time.Hour), // Cooldown passed
		}
		mockRepo.EXPECT().
			GetLastLogValue(ctx, trigger.ID, eventData.VehicleDID).
			Return(lastLog, nil).
			Times(1)

		result, err := evaluator.EvaluateEventTrigger(ctx, trigger, program, eventData)

		require.NoError(t, err)
		require.NotNil(t, result)
		assert.False(t, result.ShouldFire)
		assert.False(t, result.PermissionDenied)
		assert.False(t, result.CoolDownNotMet)
		assert.True(t, result.ConditionNotMet)
	})

	t.Run("no previous log - first trigger", func(t *testing.T) {
		ctrl := gomock.NewController(t)
		defer ctrl.Finish()

		mockRepo := NewMockTriggerRepo(ctrl)
		mockTokenClient := NewMockTokenExchangeClient(ctrl)
		evaluator := NewTriggerEvaluator(mockRepo, mockTokenClient)

		ctx := context.Background()
		trigger := createTestTrigger()
		eventData := createTestEventData()
		program, err := celcondition.PrepareEventCondition("durationNs > previousDurationNs")
		require.NoError(t, err)

		// Mock permission check - success
		expectedPermissions := []string{
			"privilege:GetNonLocationHistory",
			"privilege:GetLocationHistory",
		}
		mockTokenClient.EXPECT().
			HasVehiclePermissions(ctx, eventData.VehicleDID, common.BytesToAddress(trigger.DeveloperLicenseAddress), expectedPermissions).
			Return(true, nil).
			Times(1)

		// Mock last log retrieval - no rows (first trigger)
		mockRepo.EXPECT().
			GetLastLogValue(ctx, trigger.ID, eventData.VehicleDID).
			Return(nil, sql.ErrNoRows).
			Times(1)

		result, err := evaluator.EvaluateEventTrigger(ctx, trigger, program, eventData)

		require.NoError(t, err)
		require.NotNil(t, result)
		assert.True(t, result.ShouldFire)
		assert.False(t, result.PermissionDenied)
		assert.False(t, result.CoolDownNotMet)
		assert.False(t, result.ConditionNotMet)
	})
}

// Helper functions for creating test data

func createTestTrigger() *models.Trigger {
	return &models.Trigger{
		ID:                      "test-trigger-id",
		Service:                 "telemetry.signals",
		MetricName:              "speed",
		Condition:               "valueNumber > 55",
		TargetURI:               "https://example.com/webhook",
		Status:                  "enabled",
		CooldownPeriod:          300, // 5 minutes
		DeveloperLicenseAddress: common.HexToAddress("0x1234567890abcdef").Bytes(),
	}
}

func createTestAssetDID() cloudevent.ERC721DID {
	return cloudevent.ERC721DID{
		ChainID:         137,
		ContractAddress: common.HexToAddress("0xbA5738a18d83D41847dfFbDC6101d37C69c9B0cF"),
		TokenID:         big.NewInt(12345),
	}
}

func snapShotFromSignal(t *testing.T, signal vss.Signal) []byte {
	json, err := json.Marshal(signal)
	require.NoError(t, err)
	return json
}

func snapShotFromEvent(t *testing.T, event vss.Event) []byte {
	json, err := json.Marshal(event)
	require.NoError(t, err)
	return json
}

func createTestSignalData() *SignalEvaluationData {
	return &SignalEvaluationData{
		Signal: vss.Signal{
			Timestamp:   time.Now().UTC(),
			ValueNumber: 60.0, // Higher than threshold in test condition
		},
		VehicleDID: createTestAssetDID(),
		Def: signals.SignalDefinition{
			Name:        "speed",
			ValueType:   signals.NumberType,
			Permissions: []string{"privilege:GetNonLocationHistory"},
		},
		RawData: json.RawMessage(`{"timestamp": "2024-01-01T12:00:00Z", "valueNumber": 60.0}`),
	}
}

func createTestEventData() *EventEvaluationData {
	return &EventEvaluationData{
		Event: vss.Event{
			Timestamp:  time.Now().UTC(),
			DurationNs: 10,
			Name:       "HarshBraking",
		},
		VehicleDID: createTestAssetDID(),
		RawData:    json.RawMessage(`{"eventType": "HarshBraking", "timestamp": "2024-01-01T12:00:00Z", "durationNs": 10}`),
	}
}
