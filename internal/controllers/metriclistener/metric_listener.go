package metriclistener

import (
	"context"
	"encoding/json"
	"fmt"
	"time"

	"github.com/DIMO-Network/cloudevent"
	"github.com/DIMO-Network/model-garage/pkg/vss"
	"github.com/ethereum/go-ethereum/common"
	"github.com/DIMO-Network/server-garage/pkg/richerrors"
	"github.com/DIMO-Network/vehicle-triggers-api/internal/config"
	"github.com/DIMO-Network/vehicle-triggers-api/internal/controllers/webhook"
	"github.com/DIMO-Network/vehicle-triggers-api/internal/db/models"
	"github.com/DIMO-Network/vehicle-triggers-api/internal/services/triggerevaluator"
	"github.com/DIMO-Network/vehicle-triggers-api/internal/services/triggersrepo"
	"github.com/DIMO-Network/vehicle-triggers-api/internal/services/webhookcache"
	"github.com/DIMO-Network/vehicle-triggers-api/internal/services/webhooksender"
	"github.com/ThreeDotsLabs/watermill/message"
	"github.com/google/cel-go/cel"
	"github.com/google/uuid"
	"github.com/rs/zerolog"
	"golang.org/x/sync/semaphore"
)

// NATSBridge republishes parsed CloudEvents onto NATS subjects derived from
// the inner signal/event name. Set on the listener when running in
// NATS-primary ingest mode so Kafka consumers stop evaluating and instead
// hand traffic to NATS consumers.
type NATSBridge interface {
	PublishSignals(ctx context.Context, ce vss.SignalCloudEvent) (int, error)
	PublishEvents(ctx context.Context, ce vss.EventCloudEvent) (int, error)
}

// AuditPublisher emits a record to the trigger-fired audit stream for every
// successful webhook delivery. Used by downstream billing/usage aggregation.
// Implementations should be non-blocking; the caller invokes them in a
// background goroutine and ignores errors.
type AuditPublisher interface {
	PublishTriggerFired(ctx context.Context, devLicense string, record []byte) error
}

// StateRecorder persists the fire record so other replicas honor cooldown
// and so the next signal's previousValue lookup sees this fire. Errors are
// logged and swallowed - the audit stream remains the durable long-term
// record.
type StateRecorder interface {
	RecordFire(ctx context.Context, triggerID, metricName string, vehicleDID cloudevent.ERC721DID, at time.Time, snapshot json.RawMessage) error
}

// TriggerRepo is the listener's narrowed view of triggersrepo. Trigger-log
// writes used to live here too but were removed when state moved to NATS KV;
// the audit stream now carries the per-fire record long-term.
type TriggerRepo interface {
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
	webhookCache     WebhookCache
	repo             TriggerRepo
	webhookSender    WebhookSender
	triggerEvaluator TriggerEvaluator
	maxFailureCount  int

	// bridge, when non-nil, makes the listener republish parsed CloudEvents
	// to NATS instead of evaluating them. Used to put Kafka consumers in
	// bridge-only mode while NATS consumers own evaluation.
	bridge NATSBridge

	// auditor, when non-nil, publishes a per-fire record to the audit stream
	// after a successful webhook delivery. Best-effort, async.
	auditor AuditPublisher

	// state, when non-nil, records the fire timestamp in the distributed
	// state store so other replicas see the cooldown immediately.
	state StateRecorder
}

// WithBridge returns a copy of the listener wired to publish parsed
// CloudEvents to NATS in place of evaluation. Callers use this for the Kafka
// consumers when NATS is the primary evaluation path.
func (m *MetricListener) WithBridge(b NATSBridge) *MetricListener {
	cp := *m
	cp.bridge = b
	return &cp
}

// WithAuditor returns a copy of the listener wired to publish trigger-fired
// audit records. Use this on the NATS-side listener so audit traffic flows
// only from the evaluation path, not from any transient bridge.
func (m *MetricListener) WithAuditor(a AuditPublisher) *MetricListener {
	cp := *m
	cp.auditor = a
	return &cp
}

// WithStateRecorder returns a copy of the listener wired to write per-fire
// state to the distributed KV bucket after successful webhook delivery.
func (m *MetricListener) WithStateRecorder(s StateRecorder) *MetricListener {
	cp := *m
	cp.state = s
	return &cp
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
		webhookCache:     wc,
		repo:             repo,
		webhookSender:    webhookSender,
		triggerEvaluator: triggerEvaluator,
		maxFailureCount:  failureCount,
	}
}

func (m *MetricListener) ProcessSignalMessages(ctx context.Context, messages <-chan *message.Message, maxInFlight int) error {
	return processMessage(ctx, messages, m.processSignalMessage, maxInFlight)
}

func (m *MetricListener) ProcessEventMessages(ctx context.Context, messages <-chan *message.Message, maxInFlight int) error {
	return processMessage(ctx, messages, m.processEventMessage, maxInFlight)
}

func processMessage(ctx context.Context, messages <-chan *message.Message, processor func(msg *message.Message) error, maxInFlight int) error {
	logger := zerolog.Ctx(ctx)
	sem := semaphore.NewWeighted(int64(maxInFlight))

	// waitForInFlight waits for all in-flight goroutines to complete
	waitForInFlight := func() {
		_ = sem.Acquire(context.Background(), int64(maxInFlight))
		sem.Release(int64(maxInFlight))
	}

	for {
		select {
		case <-ctx.Done():
			waitForInFlight()
			return ctx.Err()
		case msg, ok := <-messages:
			if !ok {
				// channel is closed, wait for all in-flight messages to complete
				waitForInFlight()
				return nil
			}
			if ctx.Err() != nil {
				// check context since select is not deterministic when multiple cases are ready
				waitForInFlight()
				return ctx.Err()
			}

			// Acquire semaphore slot before processing
			if err := sem.Acquire(ctx, 1); err != nil {
				// Context cancelled while waiting for slot, wait for in-flight to complete
				waitForInFlight()
				return ctx.Err()
			}

			msg.SetContext(ctx)
			go func(m *message.Message) {
				defer sem.Release(1)
				if err := processor(m); err != nil {
					logger.Error().Err(err).Msg("error processing message")
				}
				m.Ack()
			}(msg)
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

	// State + audit are the new long-term record. State is written first so
	// the cooldown takes effect immediately for the next signal on any
	// replica; audit is async best-effort.
	m.recordState(ctx, trigger, payload, metricData)
	m.publishAudit(ctx, trigger, payload)

	return nil
}

// recordState writes the fire to the distributed state store so other
// replicas honor the cooldown immediately and the next previousValue lookup
// sees this fire's snapshot. Errors are logged - the audit stream remains
// the long-term record.
func (m *MetricListener) recordState(ctx context.Context, trigger *models.Trigger, payload *cloudevent.CloudEvent[webhook.WebhookPayload], snapshot json.RawMessage) {
	if m.state == nil {
		return
	}
	if err := m.state.RecordFire(ctx, trigger.ID, trigger.MetricName, payload.Data.AssetDID, time.Now().UTC(), snapshot); err != nil {
		zerolog.Ctx(ctx).Warn().Err(err).Str("triggerId", trigger.ID).Msg("state recorder: write failed")
	}
}

// publishAudit emits a record to the trigger-fired audit stream. Runs in a
// detached goroutine and swallows errors - audit loss must never block
// webhook delivery. Skipped silently when no auditor is configured.
func (m *MetricListener) publishAudit(ctx context.Context, trigger *models.Trigger, payload *cloudevent.CloudEvent[webhook.WebhookPayload]) {
	if m.auditor == nil {
		return
	}
	devLicense := common.BytesToAddress(trigger.DeveloperLicenseAddress).Hex()
	record, err := json.Marshal(payload)
	if err != nil {
		zerolog.Ctx(ctx).Warn().Err(err).Str("triggerId", trigger.ID).Msg("audit: marshal failed")
		return
	}
	auditor := m.auditor
	go func() {
		// Detach from request ctx so a slow audit publish or cancelled
		// upstream consumer can't drop the record. Hard cap so a wedged
		// NATS server can't leak goroutines forever.
		bgCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		if err := auditor.PublishTriggerFired(bgCtx, devLicense, record); err != nil {
			zerolog.Ctx(ctx).Warn().Err(err).Str("triggerId", trigger.ID).Msg("audit publish failed")
		}
	}()
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
			DataVersion:     trigger.Service + "/v1.0",
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
