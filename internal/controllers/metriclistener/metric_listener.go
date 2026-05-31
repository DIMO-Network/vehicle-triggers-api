package metriclistener

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"time"

	"github.com/DIMO-Network/cloudevent"
	"github.com/DIMO-Network/vehicle-triggers-api/internal/config"
	"github.com/DIMO-Network/vehicle-triggers-api/internal/controllers/webhook"
	"github.com/DIMO-Network/vehicle-triggers-api/internal/db/models"
	"github.com/DIMO-Network/vehicle-triggers-api/internal/services/triggerevaluator"
	"github.com/DIMO-Network/vehicle-triggers-api/internal/services/triggersrepo"
	"github.com/DIMO-Network/vehicle-triggers-api/internal/services/webhookcache"
	"github.com/DIMO-Network/vehicle-triggers-api/internal/services/webhookdispatcher"
	"github.com/google/cel-go/cel"
	"github.com/google/uuid"
)

// TriggerRepo is the listener's narrowed view of triggersrepo. The listener
// only deletes vehicle subscriptions when permissions are revoked; failure-
// count writes and reset live with the dispatcher post-Kafka deletion.
type TriggerRepo interface {
	DeleteVehicleSubscription(ctx context.Context, triggerID string, assetDid cloudevent.ERC721DID) (int64, error)
}

type TriggerEvaluator interface {
	EvaluateSignalTrigger(ctx context.Context, trigger *models.Trigger, program cel.Program, signal *triggerevaluator.SignalEvaluationData) (*triggerevaluator.TriggerEvaluationResult, error)
	EvaluateEventTrigger(ctx context.Context, trigger *models.Trigger, program cel.Program, ev *triggerevaluator.EventEvaluationData) (*triggerevaluator.TriggerEvaluationResult, error)
}

type WebhookCache interface {
	GetWebhooks(vehicleDID string, service string, metricName string) []*webhookcache.Webhook
	ScheduleRefresh(ctx context.Context)
	InvalidateVehicleTrigger(assetDID, triggerID string)
}

// WebhookDispatcher is the sink for evaluated triggers. The production
// implementation lives in internal/services/webhookdispatcher and owns
// send + state + audit + failure-count bookkeeping.
type WebhookDispatcher interface {
	Enqueue(ctx context.Context, j webhookdispatcher.Job) error
}

// MetricListener parses inbound CloudEvent payloads, runs each matching
// trigger's CEL condition against the per-vehicle state snapshot, and hands
// firing triggers off to the dispatcher.
//
// Post-Kafka rip the listener owns only evaluation; delivery, state, audit,
// and circuit-breaker live in webhookdispatcher.Dispatcher. The dispatcher
// is required - inline delivery has been removed because it duplicated
// every responsibility the dispatcher already owned.
type MetricListener struct {
	webhookCache     WebhookCache
	repo             TriggerRepo
	triggerEvaluator TriggerEvaluator
	dispatcher       WebhookDispatcher
	maxFailureCount  int
}

// NewMetricsListener constructs a listener wired to the supplied dispatcher.
// The dispatcher may be nil in unit tests that don't exercise the fire path;
// fanoutAndFire short-circuits when no webhook subscribers match.
func NewMetricsListener(wc WebhookCache,
	repo TriggerRepo,
	triggerEvaluator TriggerEvaluator,
	dispatcher WebhookDispatcher,
	settings *config.Settings,
) *MetricListener {
	failureCount := int(settings.MaxWebhookFailureCount)
	if failureCount < 1 {
		failureCount = 1
	}
	return &MetricListener{
		webhookCache:     wc,
		repo:             repo,
		triggerEvaluator: triggerEvaluator,
		dispatcher:       dispatcher,
		maxFailureCount:  failureCount,
	}
}

// handleTriggeredWebhook is the post-eval hand-off: the circuit-breaker
// check stays here so we never enqueue work for disabled/failed triggers,
// then we Enqueue. ErrQueueFull bubbles back to the JetStream handler which
// nak's it with the long backpressure delay.
func (m *MetricListener) handleTriggeredWebhook(ctx context.Context, trigger *models.Trigger, metricData []byte, payload *cloudevent.CloudEvent[webhook.WebhookPayload]) error {
	if !m.ShouldAttemptWebhook(trigger) {
		return nil
	}
	if m.dispatcher == nil {
		// Test mode: no dispatcher wired. Silently swallow so tests that
		// exercise only the eval path don't have to thread a mock through.
		return nil
	}
	return m.dispatcher.Enqueue(ctx, webhookdispatcher.Job{
		Trigger:    trigger,
		Payload:    payload,
		Snapshot:   metricData,
		MetricName: trigger.MetricName,
		VehicleDID: payload.Data.AssetDID,
	})
}

// createWebhookPayload creates a standardized webhook payload. The CloudEvent
// ID is derived deterministically from (triggerID, sourceID) so receivers
// can dedup across JetStream redelivery: the same source signal/event
// re-evaluated by another replica produces the same webhook ID. Falls back
// to a random UUID when sourceID is empty (defensive; the call sites all
// pass a CloudEvent ID from the inbound payload).
func (m *MetricListener) createWebhookPayload(trigger *models.Trigger, assetDid cloudevent.ERC721DID, sourceID string) *cloudevent.CloudEvent[webhook.WebhookPayload] {
	id := webhookID(trigger.ID, sourceID)
	payload := &cloudevent.CloudEvent[webhook.WebhookPayload]{
		CloudEventHeader: cloudevent.CloudEventHeader{
			ID: id,
			// Source is hardcoded today. The right value is the 0x address
			// of the DIMO storage node that emitted the signal/event we
			// fired on - that's the only identity that lets receivers
			// verify CloudEvent provenance. Blocked on storage-node
			// identity being available to the evaluator at runtime; tracked
			// in PROD_HARDENING_V2.md item P. Until that lands, "vehicle-
			// triggers-api" stamps the dispatcher service identity, which
			// is at least stable.
			Source:          "vehicle-triggers-api",
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
			WebhookID:   trigger.ID,
			WebhookName: trigger.DisplayName,
			AssetDID:    assetDid,
			Condition:   trigger.Condition,
		},
	}
	return payload
}

// webhookID returns a deterministic CloudEvent ID for a fire. The output is
// stable across JetStream redelivery so receivers can deduplicate.
func webhookID(triggerID, sourceID string) string {
	if sourceID == "" {
		return uuid.New().String()
	}
	sum := sha256.Sum256([]byte(triggerID + "|" + sourceID))
	return hex.EncodeToString(sum[:16]) // 128 bits is plenty for collision-safe dedup
}

// ShouldAttemptWebhook is the circuit-breaker check: skip enqueue when the
// trigger is disabled, deleted, or at the failure threshold.
func (m *MetricListener) ShouldAttemptWebhook(trigger *models.Trigger) bool {
	if trigger.Status != triggersrepo.StatusEnabled {
		return false
	}
	if trigger.FailureCount >= m.maxFailureCount {
		return false
	}
	return true
}
