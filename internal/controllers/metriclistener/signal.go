package metriclistener

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"

	"github.com/DIMO-Network/cloudevent"
	"github.com/DIMO-Network/model-garage/pkg/vss"
	"github.com/DIMO-Network/vehicle-triggers-api/internal/controllers/webhook"
	"github.com/DIMO-Network/vehicle-triggers-api/internal/db/models"
	"github.com/DIMO-Network/vehicle-triggers-api/internal/services/triggerevaluator"
	"github.com/DIMO-Network/vehicle-triggers-api/internal/services/triggersrepo"
	"github.com/DIMO-Network/vehicle-triggers-api/internal/signals"
)

// HandleSignalPayload parses a SignalCloudEvent and evaluates triggers
// against each unpacked signal. The NATS pull-loop entry point.
func (m *MetricListener) HandleSignalPayload(ctx context.Context, payload []byte) error {
	var signalCE vss.SignalCloudEvent
	if err := json.Unmarshal(payload, &signalCE); err != nil {
		return fmt.Errorf("failed to parse signal CloudEvent: %w", err)
	}

	sigs := vss.UnpackSignals(signalCE)

	vehicleDID, err := cloudevent.DecodeERC721DID(signalCE.Subject)
	if err != nil {
		return fmt.Errorf("failed to decode ERC721DID from envelope: %w", err)
	}

	var errs error
	for _, sig := range sigs {
		sigData, err := json.Marshal(sig)
		if err != nil {
			errs = errors.Join(errs, fmt.Errorf("failed to marshal signal: %w", err))
			continue
		}
		if err := m.processSingleSignal(ctx, sig, vehicleDID, sigData); err != nil {
			errs = errors.Join(errs, err)
		}
	}
	return errs
}

// inferSignalValueType returns the value type for payload/CEL when the signal is not in the schema.
func inferSignalValueType(sig vss.Signal) string {
	if sig.Data.ValueLocation != (vss.Location{}) {
		return signals.LocationType
	}
	if sig.Data.ValueString != "" {
		return signals.StringType
	}
	return signals.NumberType
}

func (m *MetricListener) processSingleSignal(ctx context.Context, sig vss.Signal, vehicleDID cloudevent.ERC721DID, rawPayload json.RawMessage) error {
	signalDef := signals.GetSignalDefinitionOrDefault(sig.Data.Name, inferSignalValueType(sig))
	eval := &triggerevaluator.SignalEvaluationData{
		Signal:     sig,
		VehicleDID: vehicleDID,
		Def:        signalDef,
		RawData:    rawPayload,
	}
	webhooks := m.webhookCache.GetWebhooks(vehicleDID.String(), triggersrepo.ServiceSignal, signals.VSSPrefix+sig.Data.Name)
	return fanoutAndFire(ctx, m, webhooks, vehicleDID, rawPayload, eval,
		m.triggerEvaluator.EvaluateSignalTrigger, m.createSignalPayload)
}

func (m *MetricListener) createSignalPayload(trigger *models.Trigger, sigEval *triggerevaluator.SignalEvaluationData) (*cloudevent.CloudEvent[webhook.WebhookPayload], error) {
	var signalValue any
	switch sigEval.Def.ValueType {
	case signals.NumberType:
		signalValue = sigEval.Signal.Data.ValueNumber
	case signals.StringType:
		signalValue = sigEval.Signal.Data.ValueString
	case signals.LocationType:
		signalValue = sigEval.Signal.Data.ValueLocation
	default:
		return nil, fmt.Errorf("unsupported signal type: %s", sigEval.Def.ValueType)
	}
	payload := m.createWebhookPayload(trigger, sigEval.VehicleDID, sigEval.Signal.ID)
	payload.Data.Signal = &webhook.SignalData{
		Name:      sigEval.Signal.Data.Name,
		Source:    sigEval.Signal.Source,
		Units:     sigEval.Def.Unit,
		Timestamp: sigEval.Signal.Data.Timestamp,
		Producer:  sigEval.Signal.Producer,
		ValueType: sigEval.Def.ValueType,
		Value:     signalValue,
	}
	return payload, nil
}
