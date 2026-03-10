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
	"github.com/DIMO-Network/vehicle-triggers-api/internal/services/webhookcache"
	"github.com/DIMO-Network/vehicle-triggers-api/internal/signals"
	"github.com/ThreeDotsLabs/watermill/message"
	"github.com/rs/zerolog"
	"golang.org/x/sync/errgroup"
)

func (m *MetricListener) processSignalMessage(msg *message.Message) error {
	var signalCE vss.SignalCloudEvent
	if err := json.Unmarshal(msg.Payload, &signalCE); err != nil {
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
		if err := m.processSingleSignal(msg.Context(), sig, vehicleDID, sigData); err != nil {
			errs = errors.Join(errs, err)
		}
	}
	return errs
}

func (m *MetricListener) processSingleSignal(ctx context.Context, sig vss.Signal, vehicleDID cloudevent.ERC721DID, rawPayload json.RawMessage) error {
	signalDef, err := signals.GetSignalDefinition(sig.Data.Name)
	if err != nil {
		zerolog.Ctx(ctx).Error().Err(err).
			Str("signal_name", sig.Data.Name).
			Str("asset_did", vehicleDID.String()).
			Str("subject", sig.Subject).
			Msg("failed to get signal definition")
		return fmt.Errorf("failed to get signal definition: %w", err)
	}

	sigAndRaw := triggerevaluator.SignalEvaluationData{
		Signal:     sig,
		VehicleDID: vehicleDID,
		Def:        signalDef,
		RawData:    rawPayload,
	}

	webhooks := m.webhookCache.GetWebhooks(vehicleDID.String(), triggersrepo.ServiceSignalVSS, sig.Data.Name)
	if len(webhooks) == 0 {
		return nil
	}

	group, groupCtx := errgroup.WithContext(ctx)
	group.SetLimit(100)
	for _, wh := range webhooks {
		group.Go(func() error {
			if err := m.processSignalWebhook(groupCtx, wh, &sigAndRaw); err != nil {
				zerolog.Ctx(groupCtx).Error().Str("trigger_id", wh.Trigger.ID).Err(err).Msg("failed to process webhook")
			}
			return nil
		})
	}
	return group.Wait()
}

func (m *MetricListener) processSignalWebhook(ctx context.Context, wh *webhookcache.Webhook, sigAndRaw *triggerevaluator.SignalEvaluationData) error {
	// Evaluate the trigger using the new service
	result, err := m.triggerEvaluator.EvaluateSignalTrigger(ctx, wh.Trigger, wh.Program, sigAndRaw)
	if err != nil {
		return fmt.Errorf("failed to evaluate signal trigger: %w", err)
	}

	if !result.ShouldFire {
		// Handle permission denied - unsubscribe from the trigger
		if result.PermissionDenied {
			_, err := m.repo.DeleteVehicleSubscription(ctx, wh.Trigger.ID, sigAndRaw.VehicleDID)
			if err != nil {
				return fmt.Errorf("failed to delete vehicle subscription: %w", err)
			}
			m.webhookCache.ScheduleRefresh(ctx)
		}
		return nil
	}

	payload, err := m.createSignalPayload(wh.Trigger, sigAndRaw)
	if err != nil {
		return fmt.Errorf("failed to create webhook payload: %w", err)
	}

	return m.handleTriggeredWebhook(ctx, wh.Trigger, sigAndRaw.RawData, payload)
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
	payload := m.createWebhookPayload(trigger, sigEval.VehicleDID)
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
