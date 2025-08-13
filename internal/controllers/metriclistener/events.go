package metriclistener

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"

	"github.com/DIMO-Network/cloudevent"
	"github.com/DIMO-Network/model-garage/pkg/vss"
	"github.com/DIMO-Network/vehicle-triggers-api/internal/celcondition"
	"github.com/DIMO-Network/vehicle-triggers-api/internal/controllers/webhook"
	"github.com/DIMO-Network/vehicle-triggers-api/internal/db/models"
	"github.com/DIMO-Network/vehicle-triggers-api/internal/services/triggersrepo"
	"github.com/DIMO-Network/vehicle-triggers-api/internal/services/webhookcache"
	"github.com/ThreeDotsLabs/watermill/message"
	"github.com/ethereum/go-ethereum/common"
	"github.com/rs/zerolog"
	"golang.org/x/sync/errgroup"
)

func (m *MetricListener) processEventMessage(msg *message.Message) error {
	rawEvents := []json.RawMessage{}
	if err := json.Unmarshal(msg.Payload, &rawEvents); err != nil {
		return fmt.Errorf("failed to parse vehicle signal JSON: %w", err)
	}
	var errs error
	for _, eventData := range rawEvents {
		if err := m.processSingleEvent(msg.Context(), eventData); err != nil {
			errs = errors.Join(errs, err)
		}
	}
	return errs
}

func (m *MetricListener) processSingleEvent(ctx context.Context, eventData json.RawMessage) error {
	eventAndRaw := EventWithRawData{
		RawData: eventData,
	}
	err := json.Unmarshal(eventData, &eventAndRaw.Event)
	if err != nil {
		return fmt.Errorf("failed to unmarshal event: %w", err)
	}

	eventAndRaw.VehicleDID, err = cloudevent.DecodeERC721DID(eventAndRaw.Event.Subject)
	if err != nil {
		return fmt.Errorf("failed to decode ERC721DID: %w", err)
	}

	webhooks := m.webhookCache.GetWebhooks(eventAndRaw.VehicleDID.String(), triggersrepo.ServiceEvent, eventAndRaw.Event.Name)
	if len(webhooks) == 0 {
		// no webhooks found for this signal, skip
		return nil
	}

	group, groupCtx := errgroup.WithContext(ctx)
	group.SetLimit(100)
	for _, wh := range webhooks {
		group.Go(func() error {
			if err := m.processEventWebhook(groupCtx, wh, &eventAndRaw); err != nil {
				zerolog.Ctx(groupCtx).Error().Str("trigger_id", wh.Trigger.ID).Err(err).Msg("failed to process webhook")
			}
			return nil
		})
	}
	return group.Wait()
}

func (m *MetricListener) processEventWebhook(ctx context.Context, wh *webhookcache.Webhook, eventAndRaw *EventWithRawData) error {
	hasPerm, err := m.tokenExchangeClient.HasVehiclePermissions(ctx, eventAndRaw.VehicleDID, common.BytesToAddress(wh.Trigger.DeveloperLicenseAddress), []string{
		"privilege:GetNonLocationHistory",
		"privilege:GetLocationHistory",
	})
	if err != nil {
		return fmt.Errorf("permission check failed: %w", err)
	}
	if !hasPerm {
		// If we don't have permission, unsubscribe from the trigger and refresh the cache
		zerolog.Ctx(ctx).Info().Msgf("permissions revoked for license %x on vehicle %s", wh.Trigger.DeveloperLicenseAddress, eventAndRaw.VehicleDID.String())
		_, err := m.repo.DeleteVehicleSubscription(ctx, wh.Trigger.ID, eventAndRaw.VehicleDID)
		if err != nil {
			return fmt.Errorf("failed to delete vehicle subscription: %w", err)
		}
		m.webhookCache.ScheduleRefresh(ctx)
		return nil
	}

	lastTrigger, err := m.getLastLogValue(ctx, wh.Trigger.ID, eventAndRaw.VehicleDID)
	if err != nil {
		return fmt.Errorf("failed to retrieve event logs: %w", err)
	}

	var previousEvent vss.Event
	if err := json.Unmarshal(lastTrigger.SnapshotData, &previousEvent); err != nil {
		return fmt.Errorf("failed to unmarshal previous event: %w", err)
	}

	shouldFire, err := celcondition.EvaluateEventCondition(wh.Program, &eventAndRaw.Event, &previousEvent)
	if err != nil {
		return fmt.Errorf("failed to evaluate CEL condition: %w", err)
	}
	if !shouldFire {
		return nil
	}

	payload := m.createEventPayload(wh.Trigger, eventAndRaw)

	return m.handleTriggeredWebhook(ctx, wh.Trigger, lastTrigger, eventAndRaw.RawData, payload)
}
func (m *MetricListener) createEventPayload(trigger *models.Trigger, eventAndRaw *EventWithRawData) *cloudevent.CloudEvent[webhook.WebhookPayload] {
	payload := m.createWebhookPayload(trigger, eventAndRaw.VehicleDID)
	payload.Data.Event = &webhook.EventData{
		Name:       eventAndRaw.Event.Name,
		Timestamp:  eventAndRaw.Event.Timestamp,
		Source:     eventAndRaw.Event.Source,
		Producer:   eventAndRaw.Event.Producer,
		DurationNs: eventAndRaw.Event.DurationNs,
		Metadata:   eventAndRaw.Event.Metadata,
	}
	return payload
}
