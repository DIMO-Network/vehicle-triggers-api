//go:generate go tool mockgen -source=trigger_evaluator.go -destination=trigger_evaluator_mock_test.go -package=triggerevaluator

package triggerevaluator

import (
	"context"
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
	"github.com/DIMO-Network/vehicle-triggers-api/internal/services/triggerstate"
	"github.com/DIMO-Network/vehicle-triggers-api/internal/signals"
	"github.com/ethereum/go-ethereum/common"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"go.uber.org/mock/gomock"
)

// noState satisfies StateStore with no records - used by tests that exercise
// permission paths where state should not be consulted.
type noState struct{}

func (noState) LastFire(context.Context, string, cloudevent.ERC721DID) (triggerstate.Record, bool, error) {
	return triggerstate.Record{}, false, nil
}

func (noState) LastMetric(context.Context, cloudevent.ERC721DID, string) (triggerstate.MetricRecord, bool, error) {
	return triggerstate.MetricRecord{}, false, nil
}

func TestNewTriggerEvaluator(t *testing.T) {
	t.Parallel()
	ctrl := gomock.NewController(t)
	mockTokenClient := NewMockTokenExchangeClient(ctrl)
	evaluator := NewTriggerEvaluator(mockTokenClient).WithStateStore(noState{})
	require.NotNil(t, evaluator)
	assert.Equal(t, mockTokenClient, evaluator.tokenClient)
	assert.NotNil(t, evaluator.state)
}

func TestTriggerEvaluator_EvaluateSignalTrigger(t *testing.T) {
	t.Parallel()

	t.Run("successful evaluation - should fire", func(t *testing.T) {
		ctrl := gomock.NewController(t)
		mockState := NewMockStateStore(ctrl)
		mockTokenClient := NewMockTokenExchangeClient(ctrl)
		evaluator := NewTriggerEvaluator(mockTokenClient).WithStateStore(mockState)

		ctx := context.Background()
		trigger := createTestTrigger()
		signalData := createTestSignalData()
		program, err := celcondition.PrepareSignalCondition("value > previousValue", signalData.Def.ValueType)
		require.NoError(t, err)

		mockTokenClient.EXPECT().
			HasVehiclePermissions(ctx, signalData.VehicleDID, common.BytesToAddress(trigger.DeveloperLicenseAddress), signalData.Def.Permissions).
			Return(true, nil)
		mockState.EXPECT().
			LastFire(ctx, trigger.ID, signalData.VehicleDID).
			Return(triggerstate.Record{LastFiredAt: time.Now().Add(-time.Hour)}, true, nil)
		// previousValue source - any trigger fired previously with value 59
		prev := snapShotFromSignal(t, vss.Signal{Data: vss.SignalData{ValueNumber: 59}})
		mockState.EXPECT().
			LastMetric(ctx, signalData.VehicleDID, trigger.MetricName).
			Return(triggerstate.MetricRecord{LastSnapshot: prev}, true, nil)

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
		mockTokenClient := NewMockTokenExchangeClient(ctrl)
		evaluator := NewTriggerEvaluator(mockTokenClient).WithStateStore(noState{})

		ctx := context.Background()
		trigger := createTestTrigger()
		signalData := createTestSignalData()
		program, err := celcondition.PrepareSignalCondition("value > previousValue", signalData.Def.ValueType)
		require.NoError(t, err)

		mockTokenClient.EXPECT().
			HasVehiclePermissions(ctx, signalData.VehicleDID, common.BytesToAddress(trigger.DeveloperLicenseAddress), signalData.Def.Permissions).
			Return(false, nil)

		result, err := evaluator.EvaluateSignalTrigger(ctx, trigger, program, signalData)
		require.NoError(t, err)
		require.NotNil(t, result)
		assert.False(t, result.ShouldFire)
		assert.True(t, result.PermissionDenied)
	})

	t.Run("permission check error", func(t *testing.T) {
		ctrl := gomock.NewController(t)
		defer ctrl.Finish()
		mockTokenClient := NewMockTokenExchangeClient(ctrl)
		evaluator := NewTriggerEvaluator(mockTokenClient).WithStateStore(noState{})

		ctx := context.Background()
		trigger := createTestTrigger()
		signalData := createTestSignalData()
		program, err := celcondition.PrepareSignalCondition("value > previousValue", signalData.Def.ValueType)
		require.NoError(t, err)

		mockTokenClient.EXPECT().
			HasVehiclePermissions(ctx, signalData.VehicleDID, common.BytesToAddress(trigger.DeveloperLicenseAddress), signalData.Def.Permissions).
			Return(false, errors.New("permission service error"))

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
		mockState := NewMockStateStore(ctrl)
		mockTokenClient := NewMockTokenExchangeClient(ctrl)
		evaluator := NewTriggerEvaluator(mockTokenClient).WithStateStore(mockState)

		ctx := context.Background()
		trigger := createTestTrigger()
		trigger.CooldownPeriod = int(time.Hour.Seconds())
		signalData := createTestSignalData()
		program, err := celcondition.PrepareSignalCondition("value > previousValue", signalData.Def.ValueType)
		require.NoError(t, err)

		mockTokenClient.EXPECT().
			HasVehiclePermissions(ctx, signalData.VehicleDID, common.BytesToAddress(trigger.DeveloperLicenseAddress), signalData.Def.Permissions).
			Return(true, nil)
		// 30 minutes ago - cooldown of 1 hour not met
		mockState.EXPECT().
			LastFire(ctx, trigger.ID, signalData.VehicleDID).
			Return(triggerstate.Record{LastFiredAt: time.Now().Add(-30 * time.Minute)}, true, nil)

		result, err := evaluator.EvaluateSignalTrigger(ctx, trigger, program, signalData)
		require.NoError(t, err)
		require.NotNil(t, result)
		assert.False(t, result.ShouldFire)
		assert.True(t, result.CoolDownNotMet)
	})

	t.Run("condition not met", func(t *testing.T) {
		ctrl := gomock.NewController(t)
		defer ctrl.Finish()
		mockState := NewMockStateStore(ctrl)
		mockTokenClient := NewMockTokenExchangeClient(ctrl)
		evaluator := NewTriggerEvaluator(mockTokenClient).WithStateStore(mockState)

		ctx := context.Background()
		trigger := createTestTrigger()
		signalData := createTestSignalData()
		program, err := celcondition.PrepareSignalCondition("value > previousValue", signalData.Def.ValueType)
		require.NoError(t, err)

		mockTokenClient.EXPECT().
			HasVehiclePermissions(ctx, signalData.VehicleDID, common.BytesToAddress(trigger.DeveloperLicenseAddress), signalData.Def.Permissions).
			Return(true, nil)
		mockState.EXPECT().
			LastFire(ctx, trigger.ID, signalData.VehicleDID).
			Return(triggerstate.Record{LastFiredAt: time.Now().Add(-time.Hour)}, true, nil)
		prev := snapShotFromSignal(t, vss.Signal{Data: vss.SignalData{ValueNumber: 60}})
		mockState.EXPECT().
			LastMetric(ctx, signalData.VehicleDID, trigger.MetricName).
			Return(triggerstate.MetricRecord{LastSnapshot: prev}, true, nil)

		result, err := evaluator.EvaluateSignalTrigger(ctx, trigger, program, signalData)
		require.NoError(t, err)
		require.NotNil(t, result)
		assert.False(t, result.ShouldFire)
		assert.True(t, result.ConditionNotMet)
	})

	t.Run("no previous state - first trigger", func(t *testing.T) {
		ctrl := gomock.NewController(t)
		defer ctrl.Finish()
		mockState := NewMockStateStore(ctrl)
		mockTokenClient := NewMockTokenExchangeClient(ctrl)
		evaluator := NewTriggerEvaluator(mockTokenClient).WithStateStore(mockState)

		ctx := context.Background()
		trigger := createTestTrigger()
		signalData := createTestSignalData()
		program, err := celcondition.PrepareSignalCondition("value > previousValue", signalData.Def.ValueType)
		require.NoError(t, err)

		mockTokenClient.EXPECT().
			HasVehiclePermissions(ctx, signalData.VehicleDID, common.BytesToAddress(trigger.DeveloperLicenseAddress), signalData.Def.Permissions).
			Return(true, nil)
		mockState.EXPECT().
			LastFire(ctx, trigger.ID, signalData.VehicleDID).
			Return(triggerstate.Record{}, false, nil)
		mockState.EXPECT().
			LastMetric(ctx, signalData.VehicleDID, trigger.MetricName).
			Return(triggerstate.MetricRecord{}, false, nil)

		result, err := evaluator.EvaluateSignalTrigger(ctx, trigger, program, signalData)
		require.NoError(t, err)
		require.NotNil(t, result)
		// value=60 > previousValue=0 (zero-valued) -> fires
		assert.True(t, result.ShouldFire)
	})
}

// TestTransitionConditions verifies isIgnitionOn (1 and 0) and obdIsPluggedIn (0) transition
// conditions fire correctly with and without an existing previous value.
func TestTransitionConditions(t *testing.T) {
	t.Parallel()

	vehicleDID := createTestAssetDID()
	perm := []string{"privilege:GetNonLocationHistory"}
	numberDef := signals.SignalDefinition{Name: "ignition", ValueType: signals.NumberType, Permissions: perm}

	type ignitionCase struct {
		name        string
		condition   string
		signalValue float64
		// previous: nil = no previous, &v = previous fire with that value
		previous   *float64
		shouldFire bool
	}
	pf := func(v float64) *float64 { return &v }
	cases := []ignitionCase{
		{
			name: "isIgnitionOn==1 with previous 0", condition: "valueNumber == 1 && valueNumber != previousValueNumber",
			signalValue: 1, previous: pf(0), shouldFire: true,
		},
		{
			name: "isIgnitionOn==1 with no previous", condition: "valueNumber == 1 && valueNumber != previousValueNumber",
			signalValue: 1, previous: nil, shouldFire: true, // 1 != 0
		},
		{
			name: "isIgnitionOn==0 with previous 1", condition: "valueNumber == 0 && valueNumber != previousValueNumber",
			signalValue: 0, previous: pf(1), shouldFire: true,
		},
		{
			name: "isIgnitionOn==0 with no previous", condition: "valueNumber == 0 && valueNumber != previousValueNumber",
			signalValue: 0, previous: nil, shouldFire: false, // 0 == 0
		},
	}

	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			ctrl := gomock.NewController(t)
			mockState := NewMockStateStore(ctrl)
			mockTokenClient := NewMockTokenExchangeClient(ctrl)
			evaluator := NewTriggerEvaluator(mockTokenClient).WithStateStore(mockState)

			trigger := &models.Trigger{
				ID: "trigger-x", Service: "signals", MetricName: "vss.isIgnitionOn",
				Condition: c.condition, CooldownPeriod: 600,
				DeveloperLicenseAddress: common.HexToAddress("0x1234567890abcdef").Bytes(),
			}
			signalData := &SignalEvaluationData{
				Signal:     vss.Signal{Data: vss.SignalData{Timestamp: time.Now().UTC(), ValueNumber: c.signalValue}},
				VehicleDID: vehicleDID,
				Def:        numberDef,
				RawData:    json.RawMessage(`{}`),
			}
			signalData.Def.Name = "isIgnitionOn"
			program, err := celcondition.PrepareSignalCondition(trigger.Condition, signals.NumberType)
			require.NoError(t, err)

			ctx := context.Background()
			mockTokenClient.EXPECT().HasVehiclePermissions(ctx, vehicleDID, gomock.Any(), perm).Return(true, nil)
			// Past cooldown
			mockState.EXPECT().LastFire(ctx, trigger.ID, vehicleDID).
				Return(triggerstate.Record{LastFiredAt: time.Now().Add(-2 * time.Hour)}, true, nil)
			if c.previous != nil {
				prev := snapShotFromSignal(t, vss.Signal{Data: vss.SignalData{ValueNumber: *c.previous}})
				mockState.EXPECT().LastMetric(ctx, vehicleDID, "vss.isIgnitionOn").
					Return(triggerstate.MetricRecord{LastSnapshot: prev}, true, nil)
			} else {
				mockState.EXPECT().LastMetric(ctx, vehicleDID, "vss.isIgnitionOn").
					Return(triggerstate.MetricRecord{}, false, nil)
			}

			result, err := evaluator.EvaluateSignalTrigger(ctx, trigger, program, signalData)
			require.NoError(t, err)
			require.NotNil(t, result)
			assert.Equal(t, c.shouldFire, result.ShouldFire, c.name)
		})
	}
}

func TestTriggerEvaluator_EvaluateEventTrigger(t *testing.T) {
	t.Parallel()

	t.Run("successful evaluation - should fire", func(t *testing.T) {
		ctrl := gomock.NewController(t)
		defer ctrl.Finish()
		mockState := NewMockStateStore(ctrl)
		mockTokenClient := NewMockTokenExchangeClient(ctrl)
		evaluator := NewTriggerEvaluator(mockTokenClient).WithStateStore(mockState)

		ctx := context.Background()
		trigger := createTestTrigger()
		eventData := createTestEventData()
		program, err := celcondition.PrepareEventCondition("durationNs > previousDurationNs")
		require.NoError(t, err)

		expectedPermissions := []string{
			"privilege:GetNonLocationHistory",
			"privilege:GetLocationHistory",
		}
		mockTokenClient.EXPECT().
			HasVehiclePermissions(ctx, eventData.VehicleDID, common.BytesToAddress(trigger.DeveloperLicenseAddress), expectedPermissions).
			Return(true, nil)

		prev := snapShotFromEvent(t, vss.Event{Data: vss.EventData{DurationNs: 5, Name: "ignition"}})
		mockState.EXPECT().
			LastFire(ctx, trigger.ID, eventData.VehicleDID).
			Return(triggerstate.Record{
				LastFiredAt:  time.Now().Add(-time.Hour),
				LastSnapshot: prev,
			}, true, nil)

		result, err := evaluator.EvaluateEventTrigger(ctx, trigger, program, eventData)
		require.NoError(t, err)
		require.NotNil(t, result)
		assert.True(t, result.ShouldFire)
	})

	t.Run("permission denied", func(t *testing.T) {
		ctrl := gomock.NewController(t)
		defer ctrl.Finish()
		mockTokenClient := NewMockTokenExchangeClient(ctrl)
		evaluator := NewTriggerEvaluator(mockTokenClient).WithStateStore(noState{})

		ctx := context.Background()
		trigger := createTestTrigger()
		eventData := createTestEventData()
		program, err := celcondition.PrepareEventCondition("durationNs > previousDurationNs")
		require.NoError(t, err)

		expectedPermissions := []string{
			"privilege:GetNonLocationHistory",
			"privilege:GetLocationHistory",
		}
		mockTokenClient.EXPECT().
			HasVehiclePermissions(ctx, eventData.VehicleDID, common.BytesToAddress(trigger.DeveloperLicenseAddress), expectedPermissions).
			Return(false, nil)

		result, err := evaluator.EvaluateEventTrigger(ctx, trigger, program, eventData)
		require.NoError(t, err)
		require.NotNil(t, result)
		assert.True(t, result.PermissionDenied)
	})

	t.Run("cooldown not met", func(t *testing.T) {
		ctrl := gomock.NewController(t)
		defer ctrl.Finish()
		mockState := NewMockStateStore(ctrl)
		mockTokenClient := NewMockTokenExchangeClient(ctrl)
		evaluator := NewTriggerEvaluator(mockTokenClient).WithStateStore(mockState)

		ctx := context.Background()
		trigger := createTestTrigger()
		trigger.CooldownPeriod = 3600
		eventData := createTestEventData()
		program, err := celcondition.PrepareEventCondition("durationNs > previousDurationNs")
		require.NoError(t, err)

		expectedPermissions := []string{
			"privilege:GetNonLocationHistory",
			"privilege:GetLocationHistory",
		}
		mockTokenClient.EXPECT().
			HasVehiclePermissions(ctx, eventData.VehicleDID, common.BytesToAddress(trigger.DeveloperLicenseAddress), expectedPermissions).
			Return(true, nil)
		mockState.EXPECT().
			LastFire(ctx, trigger.ID, eventData.VehicleDID).
			Return(triggerstate.Record{LastFiredAt: time.Now().Add(-30 * time.Minute)}, true, nil)

		result, err := evaluator.EvaluateEventTrigger(ctx, trigger, program, eventData)
		require.NoError(t, err)
		require.NotNil(t, result)
		assert.True(t, result.CoolDownNotMet)
	})

	t.Run("no previous log - first trigger", func(t *testing.T) {
		ctrl := gomock.NewController(t)
		defer ctrl.Finish()
		mockState := NewMockStateStore(ctrl)
		mockTokenClient := NewMockTokenExchangeClient(ctrl)
		evaluator := NewTriggerEvaluator(mockTokenClient).WithStateStore(mockState)

		ctx := context.Background()
		trigger := createTestTrigger()
		eventData := createTestEventData()
		program, err := celcondition.PrepareEventCondition("durationNs > previousDurationNs")
		require.NoError(t, err)

		expectedPermissions := []string{
			"privilege:GetNonLocationHistory",
			"privilege:GetLocationHistory",
		}
		mockTokenClient.EXPECT().
			HasVehiclePermissions(ctx, eventData.VehicleDID, common.BytesToAddress(trigger.DeveloperLicenseAddress), expectedPermissions).
			Return(true, nil)
		mockState.EXPECT().
			LastFire(ctx, trigger.ID, eventData.VehicleDID).
			Return(triggerstate.Record{}, false, nil)

		result, err := evaluator.EvaluateEventTrigger(ctx, trigger, program, eventData)
		require.NoError(t, err)
		require.NotNil(t, result)
		// durationNs=10 > previousDurationNs=0 -> fires
		assert.True(t, result.ShouldFire)
	})
}

// Helper functions for creating test data

func createTestTrigger() *models.Trigger {
	return &models.Trigger{
		ID:                      "test-trigger-id",
		Service:                 "signals",
		MetricName:              "vss.speed",
		Condition:               "valueNumber > 55",
		TargetURI:               "https://example.com/webhook",
		Status:                  "enabled",
		CooldownPeriod:          300,
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
	b, err := json.Marshal(signal)
	require.NoError(t, err)
	return b
}

func snapShotFromEvent(t *testing.T, event vss.Event) []byte {
	b, err := json.Marshal(event)
	require.NoError(t, err)
	return b
}

func createTestSignalData() *SignalEvaluationData {
	return &SignalEvaluationData{
		Signal: vss.Signal{
			Data: vss.SignalData{
				Timestamp:   time.Now().UTC(),
				ValueNumber: 60.0,
			},
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
			Data: vss.EventData{
				Timestamp:  time.Now().UTC(),
				DurationNs: 10,
				Name:       "HarshBraking",
			},
		},
		VehicleDID: createTestAssetDID(),
		RawData:    json.RawMessage(`{"eventType": "HarshBraking", "timestamp": "2024-01-01T12:00:00Z", "durationNs": 10}`),
	}
}
