package metriclistener

import (
	"context"
	"encoding/json"
	"fmt"
	"time"

	"github.com/DIMO-Network/cloudevent"
	"github.com/DIMO-Network/server-garage/pkg/richerrors"
	"github.com/DIMO-Network/vehicle-triggers-api/internal/config"
	"github.com/DIMO-Network/vehicle-triggers-api/internal/controllers/webhook"
	"github.com/DIMO-Network/vehicle-triggers-api/internal/db/models"
	"github.com/DIMO-Network/vehicle-triggers-api/internal/services/triggerevaluator"
	"github.com/DIMO-Network/vehicle-triggers-api/internal/services/triggersrepo"
	"github.com/DIMO-Network/vehicle-triggers-api/internal/services/webhookcache"
	"github.com/DIMO-Network/vehicle-triggers-api/internal/services/webhooksender"
	"github.com/ThreeDotsLabs/watermill/message"
	"github.com/aarondl/sqlboiler/v4/types"
	"github.com/ethereum/go-ethereum/common"
	"github.com/google/cel-go/cel"
	"github.com/google/uuid"
	"github.com/rs/zerolog"
)

type TriggerRepo interface {
	CreateTriggerLog(ctx context.Context, triggerLog *models.TriggerLog) error
	DeleteVehicleSubscription(ctx context.Context, triggerID string, assetDid cloudevent.ERC721DID) (int64, error)
	ResetTriggerFailureCount(ctx context.Context, trigger *models.Trigger) error
	IncrementTriggerFailureCount(ctx context.Context, trigger *models.Trigger, failureReason error, maxFailureCount int) error
}

type WebhookSender interface {
	SendWebhook(ctx context.Context, trigger *models.Trigger, payload *cloudevent.CloudEvent[webhook.WebhookPayload]) error
}

type WebhookFailureManager interface {
	ShouldAttemptWebhook(trigger *models.Trigger) bool
	HandleWebhookSuccess(ctx context.Context, trigger *models.Trigger) error
	HandleWebhookFailure(ctx context.Context, trigger *models.Trigger, failureReason error) error
}

type TriggerEvaluator interface {
	EvaluateSignalTrigger(ctx context.Context, trigger *models.Trigger, program cel.Program, signal *triggerevaluator.SignalEvaluationData) (*triggerevaluator.TriggerEvaluationResult, error)
	EvaluateEventTrigger(ctx context.Context, trigger *models.Trigger, program cel.Program, ev *triggerevaluator.EventEvaluationData) (*triggerevaluator.TriggerEvaluationResult, error)
}

type WebhookCache interface {
	GetWebhooks(vehicleDID string, service string, metricName string) []*webhookcache.Webhook
	ScheduleRefresh(ctx context.Context)
}

type MetricListener struct {
	webhookCache        WebhookCache
	repo                TriggerRepo
	webhookSender       WebhookSender
	triggerEvaluator    TriggerEvaluator
	vehicleNFTAddress   common.Address
	dimoRegistryChainID uint64
	maxFailureCount     int
}

// NewMetricsListener creates a new MetrticListener.
func NewMetricsListener(wc WebhookCache,
	repo TriggerRepo,
	webhookSender WebhookSender,
	triggerEvaluator TriggerEvaluator,
	settings *config.Settings,
) *MetricListener {
	failureCount := int(settings.MaxWebhookFailureCount)
	if failureCount < 1 {
		failureCount = 1
	}
	return &MetricListener{
		webhookCache:        wc,
		repo:                repo,
		webhookSender:       webhookSender,
		triggerEvaluator:    triggerEvaluator,
		vehicleNFTAddress:   settings.VehicleNFTAddress,
		dimoRegistryChainID: settings.DIMORegistryChainID,
		maxFailureCount:     failureCount,
	}
}

func (m *MetricListener) ProcessSignalMessages(ctx context.Context, messages <-chan *message.Message) error {
	return processMessage(ctx, messages, m.processSignalMessage)
}

func (m *MetricListener) ProcessEventMessages(ctx context.Context, messages <-chan *message.Message) error {
	return processMessage(ctx, messages, m.processEventMessage)
}

func processMessage(ctx context.Context, messages <-chan *message.Message, processor func(msg *message.Message) error) error {
	logger := zerolog.Ctx(ctx)
	for {
		select {
		case <-ctx.Done():
			return ctx.Err()
		case msg, ok := <-messages:
			if !ok {
				// channel is closed
				return nil
			}
			if ctx.Err() != nil {
				// check context since select is not deterministic when multiple cases are ready
				return ctx.Err()
			}
			msg.SetContext(ctx)
			if err := processor(msg); err != nil {
				logger.Error().Err(err).Msg("error processing signal message")
			}
			msg.Ack()
		}
	}
}

func (m *MetricListener) handleTriggeredWebhook(ctx context.Context, trigger *models.Trigger, metricData json.RawMessage, payload *cloudevent.CloudEvent[webhook.WebhookPayload]) error {
	// Check if we should attempt the webhook (circuit breaker logic)
	if !m.ShouldAttemptWebhook(trigger) {
		return nil
	}

	// Send the webhook
	err := m.webhookSender.SendWebhook(ctx, trigger, payload)
	if err != nil {
		// Check if it's a webhook-specific failure
		if richError, ok := richerrors.AsRichError(err); ok && richError.Code == webhooksender.WebhookFailureCode {
			if failErr := m.repo.IncrementTriggerFailureCount(ctx, trigger, err, m.maxFailureCount); failErr != nil {
				zerolog.Ctx(ctx).Error().Err(failErr).Str("triggerId", trigger.ID).Msg("failed to handle webhook failure")
			}
			return fmt.Errorf("webhook delivery failed: %w", err)
		}
		return fmt.Errorf("failed to send webhook: %w", err)
	}

	if trigger.FailureCount > 0 {
		// If this webhook was previously failed, reset the failure count.
		if err := m.repo.ResetTriggerFailureCount(ctx, trigger); err != nil {
			zerolog.Ctx(ctx).Error().Err(err).Str("triggerId", trigger.ID).Msg("failed to handle webhook success")
		}
	}

	// Log the successful trigger
	if err := m.logWebhookTrigger(ctx, payload, metricData); err != nil {
		return fmt.Errorf("failed to log webhook trigger: %w", err)
	}

	return nil
}

func (m *MetricListener) logWebhookTrigger(ctx context.Context, payload *cloudevent.CloudEvent[webhook.WebhookPayload], metricData json.RawMessage) error {
	now := time.Now().UTC()
	eventLog := &models.TriggerLog{
		ID:              payload.ID,
		TriggerID:       payload.Data.WebhookId,
		AssetDid:        payload.Data.AssetDID.String(),
		SnapshotData:    types.JSON(metricData),
		LastTriggeredAt: now,
		CreatedAt:       now,
	}
	if err := m.repo.CreateTriggerLog(ctx, eventLog); err != nil {
		return fmt.Errorf("failed to create trigger log: %w", err)
	}
	return nil
}

// createWebhookPayload creates a standardized webhook payload following industry best practices
func (m *MetricListener) createWebhookPayload(trigger *models.Trigger, assetDid cloudevent.ERC721DID) *cloudevent.CloudEvent[webhook.WebhookPayload] {
	payload := &cloudevent.CloudEvent[webhook.WebhookPayload]{
		CloudEventHeader: cloudevent.CloudEventHeader{
			ID:              uuid.New().String(),
			Source:          "vehicle-triggers-api", //TODO(kevin): Should be 0x of the storageNode
			Subject:         assetDid.String(),
			Time:            time.Now().UTC(),
			DataContentType: "application/json",
			DataVersion:     "telemetry.signals/v1.0",
			Type:            "dimo.trigger",
			SpecVersion:     "1.0",
		},
		Data: webhook.WebhookPayload{
			Service:     trigger.Service,
			MetricName:  trigger.MetricName,
			WebhookId:   trigger.ID,
			WebhookName: trigger.DisplayName,
			AssetDID:    assetDid,
			Condition:   trigger.Condition,
		},
	}
	return payload
}

// ShouldAttemptWebhook checks if a webhook should be attempted based on its current state
func (m *MetricListener) ShouldAttemptWebhook(trigger *models.Trigger) bool {
	// Don't attempt if webhook is disabled or failed
	if trigger.Status != triggersrepo.StatusEnabled {
		return false
	}

	// Don't attempt if already at failure threshold
	if trigger.FailureCount >= m.maxFailureCount {
		return false
	}

	return true
}
