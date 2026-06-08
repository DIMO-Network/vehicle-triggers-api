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
)

// HandleEventPayload parses an EventCloudEvent and evaluates triggers
// against each unpacked event. The NATS pull-loop entry point.
func (m *MetricListener) HandleEventPayload(ctx context.Context, payload []byte) error {
	var eventCE vss.EventCloudEvent
	if err := json.Unmarshal(payload, &eventCE); err != nil {
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
		if err := m.processSingleEvent(ctx, event, eventData); err != nil {
			errs = errors.Join(errs, err)
		}
	}
	return errs
}

func (m *MetricListener) processSingleEvent(ctx context.Context, event vss.Event, rawPayload json.RawMessage) error {
	vehicleDID, err := cloudevent.DecodeERC721DID(event.Subject)
	if err != nil {
		return fmt.Errorf("failed to decode ERC721DID: %w", err)
	}
	eval := &triggerevaluator.EventEvaluationData{
		Event:      event,
		VehicleDID: vehicleDID,
		RawData:    rawPayload,
	}
	webhooks := m.webhookCache.GetWebhooks(vehicleDID.String(), triggersrepo.ServiceEvent, event.Data.Name)
	return fanoutAndFire(ctx, m, webhooks, vehicleDID, rawPayload, eval,
		m.triggerEvaluator.EvaluateEventTrigger, m.createEventPayload)
}

func (m *MetricListener) createEventPayload(trigger *models.Trigger, eventEval *triggerevaluator.EventEvaluationData) (*cloudevent.CloudEvent[webhook.WebhookPayload], error) {
	payload := m.createWebhookPayload(trigger, eventEval.VehicleDID, eventEval.Event.ID)
	payload.Data.Event = &webhook.EventData{
		Name:       eventEval.Event.Data.Name,
		Timestamp:  eventEval.Event.Data.Timestamp,
		Source:     eventEval.Event.Source,
		Producer:   eventEval.Event.Producer,
		DurationNs: eventEval.Event.Data.DurationNs,
		Metadata:   eventEval.Event.Data.Metadata,
	}
	return payload, nil
}
