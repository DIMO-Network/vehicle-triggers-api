package triggerevaluator

import (
	"context"
	"encoding/json"
	"net/http"
	"time"

	"github.com/DIMO-Network/cloudevent"
	"github.com/DIMO-Network/model-garage/pkg/vss"
	"github.com/DIMO-Network/server-garage/pkg/richerrors"
	"github.com/DIMO-Network/vehicle-triggers-api/internal/celcondition"
	"github.com/DIMO-Network/vehicle-triggers-api/internal/db/models"
	"github.com/DIMO-Network/vehicle-triggers-api/internal/services/triggerstate"
	"github.com/DIMO-Network/vehicle-triggers-api/internal/signals"
	"github.com/ethereum/go-ethereum/common"
	"github.com/google/cel-go/cel"
)

// SignalEvaluationData is a struct that contains the data needed to evaluate a signal trigger.
type SignalEvaluationData struct {
	Signal     vss.Signal
	VehicleDID cloudevent.ERC721DID
	Def        signals.SignalDefinition
	RawData    json.RawMessage
}

// EventEvaluationData is a struct that contains the data needed to evaluate an event trigger.
type EventEvaluationData struct {
	Event      vss.Event
	VehicleDID cloudevent.ERC721DID
	RawData    json.RawMessage
}

// StateStore is the evaluator's read-only view of the NATS KV state buckets.
// LastFire drives the cooldown check and the per-trigger previousEvent input;
// LastMetric drives the cross-trigger previousValue input on the signal path.
// A nil store, a miss, or a transport error all degrade to "no prior data"
// so evaluation never blocks on NATS - the cost is one redundant fire when
// state is unavailable, which is acceptable given at-least-once delivery.
type StateStore interface {
	LastFire(ctx context.Context, triggerID string, vehicleDID cloudevent.ERC721DID) (triggerstate.Record, bool, error)
	LastMetric(ctx context.Context, vehicleDID cloudevent.ERC721DID, metricName string) (triggerstate.MetricRecord, bool, error)
}

// TriggerEvaluator handles trigger condition evaluation and related logic
type TriggerEvaluator struct {
	tokenClient TokenExchangeClient
	state       StateStore
}

type TriggerEvaluationResult struct {
	ShouldFire       bool
	CoolDownNotMet   bool
	PermissionDenied bool
	ConditionNotMet  bool
}

// TokenExchangeClient interface for permission checking
type TokenExchangeClient interface {
	HasVehiclePermissions(ctx context.Context, vehicleDID cloudevent.ERC721DID, developerLicense common.Address, permissions []string) (bool, error)
}

// NewTriggerEvaluator creates a new TriggerEvaluator. State is required for
// production wiring; tests can pass nil to exercise the zero-previous-value
// path. The DB-backed previousValue / cooldown fallback was removed - all
// state lives in NATS now (see internal/services/triggerstate).
func NewTriggerEvaluator(t TokenExchangeClient) *TriggerEvaluator {
	return &TriggerEvaluator{tokenClient: t}
}

// WithStateStore returns a copy of the evaluator wired to a state store.
func (t *TriggerEvaluator) WithStateStore(s StateStore) *TriggerEvaluator {
	cp := *t
	cp.state = s
	return &cp
}

// EvaluateSignalTrigger evaluates a signal trigger and returns whether it should fire.
func (t *TriggerEvaluator) EvaluateSignalTrigger(ctx context.Context, trigger *models.Trigger, program cel.Program, signal *SignalEvaluationData) (*TriggerEvaluationResult, error) {
	hasPerm, err := t.tokenClient.HasVehiclePermissions(ctx, signal.VehicleDID, common.BytesToAddress(trigger.DeveloperLicenseAddress), signal.Def.Permissions)
	if err != nil {
		return nil, richerrors.Error{
			Code:        http.StatusInternalServerError,
			Err:         err,
			ExternalMsg: "failed to check permissions for signal trigger",
		}
	}
	if !hasPerm {
		return &TriggerEvaluationResult{
			ShouldFire:       false,
			PermissionDenied: true,
		}, nil
	}

	lastFired := t.lookupLastFireTime(ctx, trigger.ID, signal.VehicleDID)
	cooldownPassed, err := t.checkCooldown(trigger, lastFired)
	if err != nil {
		return nil, richerrors.Error{
			Code:        http.StatusInternalServerError,
			Err:         err,
			ExternalMsg: "failed to check cooldown",
		}
	}
	if !cooldownPassed {
		return &TriggerEvaluationResult{
			ShouldFire:     false,
			CoolDownNotMet: true,
		}, nil
	}

	// Previous signal for CEL transition conditions: the snapshot from the
	// most recent fire of any trigger on this (vehicle, metric). Comes from
	// the signal_history KV bucket. Miss = zero-valued previousSignal, same
	// as the first-time-fire case.
	previousSignal := t.lookupPreviousSignal(ctx, signal.VehicleDID, trigger.MetricName)

	conditionMet, err := celcondition.EvaluateSignalCondition(program, &signal.Signal, &previousSignal, signal.Def.ValueType)
	if err != nil {
		return nil, richerrors.Error{
			Code:        http.StatusInternalServerError,
			Err:         err,
			ExternalMsg: "failed to evaluate CEL condition for signal trigger",
		}
	}
	if !conditionMet {
		return &TriggerEvaluationResult{
			ShouldFire:      false,
			ConditionNotMet: true,
		}, nil
	}

	return &TriggerEvaluationResult{
		ShouldFire: true,
	}, nil
}

// EvaluateEventTrigger evaluates an event trigger and returns whether it should fire.
func (t *TriggerEvaluator) EvaluateEventTrigger(ctx context.Context, trigger *models.Trigger, program cel.Program, ev *EventEvaluationData) (*TriggerEvaluationResult, error) {
	hasPerm, err := t.tokenClient.HasVehiclePermissions(ctx, ev.VehicleDID, common.BytesToAddress(trigger.DeveloperLicenseAddress), signals.DefaultPermissions)
	if err != nil {
		return nil, richerrors.Error{
			Code:        http.StatusInternalServerError,
			Err:         err,
			ExternalMsg: "failed to check permissions for event trigger",
		}
	}
	if !hasPerm {
		return &TriggerEvaluationResult{
			ShouldFire:       false,
			PermissionDenied: true,
		}, nil
	}

	// trigger_state stores both cooldown timestamp and the prior fire's
	// payload, so one KV read covers both event-side needs.
	lastFired, previousEvent := t.lookupPreviousEvent(ctx, trigger.ID, ev.VehicleDID)

	cooldownPassed, err := t.checkCooldown(trigger, lastFired)
	if err != nil {
		return nil, richerrors.Error{
			Code:        http.StatusInternalServerError,
			Err:         err,
			ExternalMsg: "failed to check cooldown for event trigger",
		}
	}
	if !cooldownPassed {
		return &TriggerEvaluationResult{
			ShouldFire:     false,
			CoolDownNotMet: true,
		}, nil
	}

	conditionMet, err := celcondition.EvaluateEventCondition(program, &ev.Event, &previousEvent)
	if err != nil {
		return nil, richerrors.Error{
			Code:        http.StatusInternalServerError,
			Err:         err,
			ExternalMsg: "failed to evaluate CEL condition for event trigger",
		}
	}
	if !conditionMet {
		return &TriggerEvaluationResult{
			ShouldFire:      false,
			ConditionNotMet: true,
		}, nil
	}

	return &TriggerEvaluationResult{
		ShouldFire: true,
	}, nil
}

// checkCooldown checks if the cooldown period has passed since the last trigger.
func (e *TriggerEvaluator) checkCooldown(t *models.Trigger, lastTriggeredAt time.Time) (bool, error) {
	if lastTriggeredAt.IsZero() {
		return true, nil
	}
	cooldown := time.Duration(t.CooldownPeriod) * time.Second
	return time.Since(lastTriggeredAt) >= cooldown, nil
}

// lookupLastFireTime returns the last fire timestamp from the trigger_state
// KV bucket. Errors and misses both return zero time so evaluation proceeds
// (matching first-time behavior).
func (t *TriggerEvaluator) lookupLastFireTime(ctx context.Context, triggerID string, vehicleDID cloudevent.ERC721DID) time.Time {
	if t.state == nil {
		return time.Time{}
	}
	rec, ok, _ := t.state.LastFire(ctx, triggerID, vehicleDID)
	if !ok {
		return time.Time{}
	}
	return rec.LastFiredAt
}

// lookupPreviousSignal returns the most recent signal payload across any
// trigger for this (vehicle, metric). Used as the previousValue input for
// transition CEL conditions like `valueNumber != previousValue`. Errors and
// misses return a zero-valued Signal; decode errors bump a metric so silent
// corruption is observable.
func (t *TriggerEvaluator) lookupPreviousSignal(ctx context.Context, vehicleDID cloudevent.ERC721DID, metricName string) vss.Signal {
	if t.state == nil {
		return vss.Signal{}
	}
	rec, ok, _ := t.state.LastMetric(ctx, vehicleDID, metricName)
	if !ok || len(rec.LastSnapshot) == 0 {
		return vss.Signal{}
	}
	var sig vss.Signal
	if err := json.Unmarshal(rec.LastSnapshot, &sig); err != nil {
		triggerstate.MetricsDecodeError("signal_history")
		return vss.Signal{}
	}
	return sig
}

// lookupPreviousEvent returns both the cooldown timestamp and the snapshot of
// the most recent fire of this trigger on this vehicle in one KV round-trip.
// Errors and misses return zero values; decode errors bump a metric.
func (t *TriggerEvaluator) lookupPreviousEvent(ctx context.Context, triggerID string, vehicleDID cloudevent.ERC721DID) (time.Time, vss.Event) {
	if t.state == nil {
		return time.Time{}, vss.Event{}
	}
	rec, ok, _ := t.state.LastFire(ctx, triggerID, vehicleDID)
	if !ok {
		return time.Time{}, vss.Event{}
	}
	var ev vss.Event
	if len(rec.LastSnapshot) > 0 {
		if err := json.Unmarshal(rec.LastSnapshot, &ev); err != nil {
			triggerstate.MetricsDecodeError("trigger_state")
		}
	}
	return rec.LastFiredAt, ev
}
