package triggerevaluator

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"net/http"
	"time"

	"github.com/DIMO-Network/cloudevent"
	"github.com/DIMO-Network/model-garage/pkg/vss"
	"github.com/DIMO-Network/server-garage/pkg/richerrors"
	"github.com/DIMO-Network/vehicle-triggers-api/internal/celcondition"
	"github.com/DIMO-Network/vehicle-triggers-api/internal/db/models"
	"github.com/DIMO-Network/vehicle-triggers-api/internal/signals"
	"github.com/ethereum/go-ethereum/common"
	"github.com/google/cel-go/cel"
)

type TriggerRepo interface {
	GetLastLogValue(ctx context.Context, triggerID string, assetDid cloudevent.ERC721DID) (*models.TriggerLog, error)
	GetLastLogForMetric(ctx context.Context, assetDid cloudevent.ERC721DID, metricName string) (*models.TriggerLog, error)
}

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

// StateStore lets the evaluator look up the most recent fire for a trigger +
// vehicle without hitting Postgres. Optional - when nil, the evaluator falls
// back to the trigger_logs table. See internal/services/triggerstate for the
// production NATS KV backend.
type StateStore interface {
	LastFire(ctx context.Context, triggerID string, vehicleDID cloudevent.ERC721DID) (time.Time, bool, error)
}

// TriggerEvaluator handles trigger condition evaluation and related logic
type TriggerEvaluator struct {
	repo        TriggerRepo
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

// NewTriggerEvaluator creates a new TriggerEvaluator
func NewTriggerEvaluator(r TriggerRepo, t TokenExchangeClient) *TriggerEvaluator {
	return &TriggerEvaluator{repo: r, tokenClient: t}
}

// WithStateStore returns a copy of the evaluator that consults state for the
// cooldown check before falling through to the trigger_logs table.
func (t *TriggerEvaluator) WithStateStore(s StateStore) *TriggerEvaluator {
	cp := *t
	cp.state = s
	return &cp
}

// EvaluateSignalTrigger evaluates a signal trigger and returns whether it should fire return true if it should fire, false if not.
func (t *TriggerEvaluator) EvaluateSignalTrigger(ctx context.Context, trigger *models.Trigger, program cel.Program, signal *SignalEvaluationData) (*TriggerEvaluationResult, error) {
	// Check permissions first
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

	// Cooldown check via KV (when wired) with DB fallback for misses/errors.
	lastFiredAt, err := t.lookupLastFire(ctx, trigger.ID, signal.VehicleDID)
	if err != nil {
		return nil, richerrors.Error{
			Code:        http.StatusInternalServerError,
			Err:         err,
			ExternalMsg: "failed to retrieve trigger logs for signal trigger",
		}
	}

	// Check cooldown
	cooldownPassed, err := t.checkCooldown(trigger, lastFiredAt)
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

	// Previous signal for condition: use last log for this (vehicle, metric) so transition-to-zero
	// conditions see the prior value (e.g. 1) from when another trigger fired, not only this trigger's log.
	lastLogForMetric, err := t.repo.GetLastLogForMetric(ctx, signal.VehicleDID, trigger.MetricName)
	if err != nil {
		return nil, richerrors.Error{
			Code:        http.StatusInternalServerError,
			Err:         err,
			ExternalMsg: "failed to retrieve last signal for metric",
		}
	}
	var previousSignal vss.Signal
	if lastLogForMetric != nil {
		if err := json.Unmarshal(lastLogForMetric.SnapshotData, &previousSignal); err != nil {
			return nil, richerrors.Error{
				Code:        http.StatusInternalServerError,
				Err:         err,
				ExternalMsg: "failed to unmarshal previous signal",
			}
		}
	}

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

// EvaluateEventTrigger evaluates an event trigger and returns whether it should fire
// Returns: shouldFire, permissionDenied, cooldownActive, error
func (t *TriggerEvaluator) EvaluateEventTrigger(ctx context.Context, trigger *models.Trigger, program cel.Program, ev *EventEvaluationData) (*TriggerEvaluationResult, error) {
	// Check permissions for events (use standard permissions)
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

	// Get last trigger log for cooldown and condition evaluation. Events
	// still need the snapshot for previous-event CEL evaluation, so we keep
	// the DB read here.
	lastTrigger, err := t.getLastLogValue(ctx, trigger.ID, ev.VehicleDID)
	if err != nil {
		return nil, richerrors.Error{
			Code:        http.StatusInternalServerError,
			Err:         err,
			ExternalMsg: "failed to retrieve trigger logs for event trigger",
		}
	}

	// Check cooldown
	cooldownPassed, err := t.checkCooldown(trigger, lastTrigger.LastTriggeredAt)
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

	// Evaluate condition
	var previousEvent vss.Event
	if err := json.Unmarshal(lastTrigger.SnapshotData, &previousEvent); err != nil {
		return nil, richerrors.Error{
			Code:        http.StatusInternalServerError,
			Err:         err,
			ExternalMsg: "failed to unmarshal previous event",
		}
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

// checkCooldown checks if the cooldown period has passed since the last trigger
func (e *TriggerEvaluator) checkCooldown(t *models.Trigger, lastTriggeredAt time.Time) (bool, error) {
	if lastTriggeredAt.IsZero() {
		return true, nil
	}
	cooldown := time.Duration(t.CooldownPeriod) * time.Second
	return time.Since(lastTriggeredAt) >= cooldown, nil
}

// lookupLastFire returns the last fire timestamp for (trigger, vehicle).
// Consults the configured KV state store first when present and falls back
// to the trigger_logs table on miss or KV error. Used only by the signal
// path; the event path keeps reading the full TriggerLog because it also
// needs the snapshot body for previous-event CEL evaluation.
func (t *TriggerEvaluator) lookupLastFire(ctx context.Context, triggerID string, vehicleDID cloudevent.ERC721DID) (time.Time, error) {
	if t.state != nil {
		when, ok, err := t.state.LastFire(ctx, triggerID, vehicleDID)
		if err == nil && ok {
			return when, nil
		}
		// Miss or error: fall through to DB so a NATS outage can't suppress
		// cooldowns.
	}
	last, err := t.getLastLogValue(ctx, triggerID, vehicleDID)
	if err != nil {
		return time.Time{}, err
	}
	return last.LastTriggeredAt, nil
}

// getLastLogValue retrieves the last trigger log for a given trigger and vehicle
func (t *TriggerEvaluator) getLastLogValue(ctx context.Context, triggerID string, assetDid cloudevent.ERC721DID) (*models.TriggerLog, error) {
	lastTrigger, err := t.repo.GetLastLogValue(ctx, triggerID, assetDid)
	if err != nil {
		// If no previous log exists, create a default one
		if errors.Is(err, sql.ErrNoRows) {
			return &models.TriggerLog{
				SnapshotData: []byte("{}"),
				AssetDid:     assetDid.String(),
				TriggerID:    triggerID,
			}, nil
		}
		return nil, err
	}
	return lastTrigger, nil
}
