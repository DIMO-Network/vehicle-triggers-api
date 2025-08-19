package metriclistener

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"

	"github.com/DIMO-Network/cloudevent"
	"github.com/DIMO-Network/vehicle-triggers-api/internal/controllers/webhook"
	"github.com/DIMO-Network/vehicle-triggers-api/internal/db/models"
	"github.com/DIMO-Network/vehicle-triggers-api/internal/services/triggerevaluator"
	"github.com/DIMO-Network/vehicle-triggers-api/internal/services/triggersrepo"
	"github.com/DIMO-Network/vehicle-triggers-api/internal/services/webhookcache"
	"github.com/ThreeDotsLabs/watermill/message"
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
	eventEval := triggerevaluator.EventEvaluationData{
		RawData: eventData,
	}
	err := json.Unmarshal(eventData, &eventEval.Event)
	if err != nil {
		return fmt.Errorf("failed to unmarshal event: %w", err)
	}

	eventEval.VehicleDID, err = cloudevent.DecodeERC721DID(eventEval.Event.Subject)
	if err != nil {
		return fmt.Errorf("failed to decode ERC721DID: %w", err)
	}

	webhooks := m.webhookCache.GetWebhooks(eventEval.VehicleDID.String(), triggersrepo.ServiceEvent, eventEval.Event.Name)
	if len(webhooks) == 0 {
		// no webhooks found for this signal, skip
		return nil
	}

	group, groupCtx := errgroup.WithContext(ctx)
	group.SetLimit(100)
	for _, wh := range webhooks {
		group.Go(func() error {
			if err := m.processEventWebhook(groupCtx, wh, &eventEval); err != nil {
				zerolog.Ctx(groupCtx).Error().Str("trigger_id", wh.Trigger.ID).Err(err).Msg("failed to process webhook")
			}
			return nil
		})
	}
	return group.Wait()
}

func (m *MetricListener) processEventWebhook(ctx context.Context, wh *webhookcache.Webhook, eventEval *triggerevaluator.EventEvaluationData) error {
	// Evaluate the trigger using the new service
	result, err := m.triggerEvaluator.EvaluateEventTrigger(ctx, wh.Trigger, wh.Program, eventEval)
	if err != nil {
		return fmt.Errorf("failed to evaluate event trigger: %w", err)
	}

	if !result.ShouldFire {
		// Handle permission denied - unsubscribe from the trigger
		if result.PermissionDenied {
			_, err := m.repo.DeleteVehicleSubscription(ctx, wh.Trigger.ID, eventEval.VehicleDID)
			if err != nil {
				return fmt.Errorf("failed to delete vehicle subscription: %w", err)
			}
			m.webhookCache.ScheduleRefresh(ctx)
		}
		return nil
	}

	payload := m.createEventPayload(wh.Trigger, eventEval)
	return m.handleTriggeredWebhook(ctx, wh.Trigger, eventEval.RawData, payload)

}
func (m *MetricListener) createEventPayload(trigger *models.Trigger, eventEval *triggerevaluator.EventEvaluationData) *cloudevent.CloudEvent[webhook.WebhookPayload] {
	payload := m.createWebhookPayload(trigger, eventEval.VehicleDID)
	payload.Data.Event = &webhook.EventData{
		Name:       eventEval.Event.Name,
		Timestamp:  eventEval.Event.Timestamp,
		Source:     eventEval.Event.Source,
		Producer:   eventEval.Event.Producer,
		DurationNs: eventEval.Event.DurationNs,
		Metadata:   eventEval.Event.Metadata,
	}
	return payload
}
