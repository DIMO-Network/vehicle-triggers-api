package metriclistener

import (
	"context"
	"encoding/json"
	"fmt"
	"math/big"

	"github.com/DIMO-Network/cloudevent"
	"github.com/DIMO-Network/model-garage/pkg/vss"
	"github.com/DIMO-Network/vehicle-triggers-api/internal/celcondition"
	"github.com/DIMO-Network/vehicle-triggers-api/internal/controllers/webhook"
	"github.com/DIMO-Network/vehicle-triggers-api/internal/db/models"
	"github.com/DIMO-Network/vehicle-triggers-api/internal/services/triggersrepo"
	"github.com/DIMO-Network/vehicle-triggers-api/internal/services/webhookcache"
	"github.com/DIMO-Network/vehicle-triggers-api/internal/signals"
	"github.com/ThreeDotsLabs/watermill/message"
	"github.com/ethereum/go-ethereum/common"
	"github.com/rs/zerolog"
	"golang.org/x/sync/errgroup"
)

// SignalWithRawData is a struct that contains a signal and the raw data.
type SignalWithRawData struct {
	Signal     vss.Signal
	VehicleDID cloudevent.ERC721DID
	Def        signals.SignalDefinition
	RawData    json.RawMessage
}

func (m *MetricListener) processSignalMessage(msg *message.Message) error {
	sigAndRaw := SignalWithRawData{
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

func (m *MetricListener) processSignalWebhook(ctx context.Context, wh *webhookcache.Webhook, sigAndRaw *SignalWithRawData) error {
	hasPerm, err := m.tokenExchangeClient.HasVehiclePermissions(ctx, sigAndRaw.VehicleDID, common.BytesToAddress(wh.Trigger.DeveloperLicenseAddress), []string{
		"privilege:GetNonLocationHistory",
		"privilege:GetLocationHistory",
	})
	if err != nil {
		return fmt.Errorf("permission check failed: %w", err)
	}
	if !hasPerm {
		// If we don't have permission, unsubscribe from the trigger and refresh the cache
		zerolog.Ctx(ctx).Info().Msgf("permissions revoked for license %x on vehicle %d", wh.Trigger.DeveloperLicenseAddress, sigAndRaw.Signal.TokenID)
		_, err := m.repo.DeleteVehicleSubscription(ctx, wh.Trigger.ID, sigAndRaw.VehicleDID)
		if err != nil {
			return fmt.Errorf("failed to delete vehicle subscription: %w", err)
		}
		m.webhookCache.ScheduleRefresh(ctx)
		return nil
	}

	lastTrigger, err := m.getLastLogValue(ctx, wh.Trigger.ID, sigAndRaw.VehicleDID)
	if err != nil {
		return fmt.Errorf("failed to retrieve event logs: %w", err)
	}

	var previousSignal vss.Signal
	if err := json.Unmarshal(lastTrigger.SnapshotData, &previousSignal); err != nil {
		return fmt.Errorf("failed to unmarshal previous signal: %w", err)
	}

	shouldFire, err := celcondition.EvaluateSignalCondition(wh.Program, &sigAndRaw.Signal, &previousSignal, sigAndRaw.Def.ValueType)
	if err != nil {
		return fmt.Errorf("failed to evaluate CEL condition: %w", err)
	}
	if !shouldFire {
		return nil
	}

	payload, err := m.createSignalPayload(wh.Trigger, sigAndRaw)
	if err != nil {
		return fmt.Errorf("failed to create webhook payload: %w", err)
	}

	return m.handleTriggeredWebhook(ctx, wh.Trigger, lastTrigger, sigAndRaw.RawData, payload)
}
func (m *MetricListener) createSignalPayload(trigger *models.Trigger, signalAndRaw *SignalWithRawData) (*cloudevent.CloudEvent[webhook.WebhookPayload], error) {
	var signalValue any
	switch signalAndRaw.Def.ValueType {
	case signals.NumberType:
		signalValue = signalAndRaw.Signal.ValueNumber
	case signals.StringType:
		signalValue = signalAndRaw.Signal.ValueString
	default:
		return nil, fmt.Errorf("unsupported signal type: %s", signalAndRaw.Def.ValueType)
	}
	payload := m.createWebhookPayload(trigger, signalAndRaw.VehicleDID)
	payload.Data.Signal = &webhook.SignalData{
		Name:      signalAndRaw.Signal.Name,
		Units:     signalAndRaw.Def.Unit,
		ValueType: signalAndRaw.Def.ValueType,
		Value:     signalValue,
	}
	return payload, nil
}
