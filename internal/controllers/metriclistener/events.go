package metriclistener

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"

	"github.com/DIMO-Network/cloudevent"
	"github.com/DIMO-Network/model-garage/pkg/vss"
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
	var eventCE vss.EventCloudEvent
	if err := json.Unmarshal(msg.Payload, &eventCE); err != nil {
		return fmt.Errorf("failed to parse event CloudEvent: %w", err)
	}

	events := vss.UnpackEvents(eventCE)

	var errs error
	for _, event := range events {
		eventData, err := json.Marshal(event)
		if err != nil {
			errs = errors.Join(errs, fmt.Errorf("failed to marshal event: %w", err))
			continue
		}
		if err := m.processSingleEvent(msg.Context(), event, eventData); err != nil {
			errs = errors.Join(errs, err)
		}
	}
	return errs
}

func (m *MetricListener) processSingleEvent(ctx context.Context, event vss.Event, rawPayload json.RawMessage) error {
	eventEval := triggerevaluator.EventEvaluationData{
		Event:   event,
		RawData: rawPayload,
	}

	var err error
	eventEval.VehicleDID, err = cloudevent.DecodeERC721DID(event.Subject)
	if err != nil {
		return fmt.Errorf("failed to decode ERC721DID: %w", err)
	}

	// Extract category from dotted name: "behavior.harshBraking" → service "events.behavior", metric "harshBraking"
	parts := strings.SplitN(event.Data.Name, ".", 2)
	service := triggersrepo.ServiceBehaviorEvent // default
	metricName := event.Data.Name
	if len(parts) == 2 {
		service = "events." + parts[0]
		metricName = parts[1]
	}

	webhooks := m.webhookCache.GetWebhooks(eventEval.VehicleDID.String(), service, metricName)
	if len(webhooks) == 0 {
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
		Name:       eventEval.Event.Data.Name,
		Timestamp:  eventEval.Event.Data.Timestamp,
		Source:     eventEval.Event.Source,
		Producer:   eventEval.Event.Producer,
		DurationNs: eventEval.Event.Data.DurationNs,
		Metadata:   eventEval.Event.Data.Metadata,
	}
	return payload
}
