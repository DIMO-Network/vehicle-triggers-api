package metriclistener

import (
	"context"
	"encoding/json"
	"fmt"
	"math/big"

	"github.com/DIMO-Network/cloudevent"
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
	sigAndRaw := triggerevaluator.SignalEvaluationData{
		RawData: json.RawMessage(msg.Payload),
	}
	if err := json.Unmarshal(sigAndRaw.RawData, &sigAndRaw.Signal); err != nil {
		return fmt.Errorf("failed to parse vehicle signal JSON: %w", err)
	}
	sigAndRaw.VehicleDID = cloudevent.ERC721DID{
		ChainID:         m.dimoRegistryChainID,
		ContractAddress: m.vehicleNFTAddress,
		TokenID:         big.NewInt(int64(sigAndRaw.Signal.TokenID)),
	}
	signalDef, err := signals.GetSignalDefinition(sigAndRaw.Signal.Name)
	if err != nil {
		zerolog.Ctx(msg.Context()).Error().Err(err).
			Str("signal_name", sigAndRaw.Signal.Name).
			Str("asset_did", sigAndRaw.VehicleDID.String()).
			Uint32("token_id", sigAndRaw.Signal.TokenID).
			Msg("failed to get signal definition")
		return fmt.Errorf("failed to get signal definition: %w", err)
	}
	sigAndRaw.Def = signalDef

	webhooks := m.webhookCache.GetWebhooks(sigAndRaw.VehicleDID.String(), triggersrepo.ServiceSignal, sigAndRaw.Signal.Name)

	if len(webhooks) == 0 {
		// no webhooks found for this signal, skip
		return nil
	}

	group, groupCtx := errgroup.WithContext(msg.Context())
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
		signalValue = sigEval.Signal.ValueNumber
	case signals.StringType:
		signalValue = sigEval.Signal.ValueString
	case signals.LocationType:
		signalValue = sigEval.Signal.ValueLocation
	default:
		return nil, fmt.Errorf("unsupported signal type: %s", sigEval.Def.ValueType)
	}
	payload := m.createWebhookPayload(trigger, sigEval.VehicleDID)
	payload.Data.Signal = &webhook.SignalData{
		Name:      sigEval.Signal.Name,
		Source:    sigEval.Signal.Source,
		Units:     sigEval.Def.Unit,
		Timestamp: sigEval.Signal.Timestamp,
		Producer:  sigEval.Signal.Producer,
		ValueType: sigEval.Def.ValueType,
		Value:     signalValue,
	}
	return payload, nil
}
