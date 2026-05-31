package metriclistener

import (
	"context"
	"encoding/json"
	"fmt"

	"github.com/DIMO-Network/cloudevent"
	"github.com/DIMO-Network/vehicle-triggers-api/internal/controllers/webhook"
	"github.com/DIMO-Network/vehicle-triggers-api/internal/db/models"
	"github.com/DIMO-Network/vehicle-triggers-api/internal/services/triggerevaluator"
	"github.com/DIMO-Network/vehicle-triggers-api/internal/services/webhookcache"
	"github.com/google/cel-go/cel"
	"github.com/rs/zerolog"
	"golang.org/x/sync/errgroup"
)

// fanoutLimit caps concurrent CEL evaluations per inbound message. Above this
// a single noisy vehicle could spawn thousands of goroutines deep into the
// trigger evaluator + permissions cache; the cache itself is thread-safe but
// the goroutine churn becomes the bottleneck.
const fanoutLimit = 100

// payloadBuilder turns an evaluation result into the outbound CloudEvent
// payload. Returning an error keeps the err-trace contract: a builder failure
// (unsupported signal type, etc.) surfaces as a per-webhook failure rather
// than poisoning the whole inbound message.
type payloadBuilder[E any] func(trigger *models.Trigger, eval *E) (*cloudevent.CloudEvent[webhook.WebhookPayload], error)

// triggerEvalFn is the per-type evaluator wired to TriggerEvaluator. Signal
// and event paths differ in input type but produce the same Result.
type triggerEvalFn[E any] func(ctx context.Context, trigger *models.Trigger, program cel.Program, eval *E) (*triggerevaluator.TriggerEvaluationResult, error)

// fanoutAndFire runs eval + post-check + fire across every webhook for the
// inbound message. Shared by the signal and event paths because the eval ->
// permission-denied -> fire flow is identical once the per-type evaluation
// data is built.
//
// Returns the aggregated error from group.Wait but the inner goroutines
// always return nil after logging - one bad webhook should never poison the
// whole fanout. Aggregation stays so the signature carries an error future-
// proofly when we want per-webhook errors back.
func fanoutAndFire[E any](
	ctx context.Context,
	m *MetricListener,
	webhooks []*webhookcache.Webhook,
	vehicleDID cloudevent.ERC721DID,
	raw json.RawMessage,
	eval *E,
	evaluate triggerEvalFn[E],
	build payloadBuilder[E],
) error {
	if len(webhooks) == 0 {
		return nil
	}
	group, groupCtx := errgroup.WithContext(ctx)
	group.SetLimit(fanoutLimit)
	for _, wh := range webhooks {
		group.Go(func() error {
			if err := evalAndFire(groupCtx, m, wh, vehicleDID, raw, eval, evaluate, build); err != nil {
				zerolog.Ctx(groupCtx).Error().Str("trigger_id", wh.Trigger.ID).Err(err).Msg("failed to process webhook")
			}
			return nil
		})
	}
	return group.Wait()
}

func evalAndFire[E any](
	ctx context.Context,
	m *MetricListener,
	wh *webhookcache.Webhook,
	vehicleDID cloudevent.ERC721DID,
	raw json.RawMessage,
	eval *E,
	evaluate triggerEvalFn[E],
	build payloadBuilder[E],
) error {
	result, err := evaluate(ctx, wh.Trigger, wh.Program, eval)
	if err != nil {
		return fmt.Errorf("failed to evaluate trigger: %w", err)
	}
	if !result.ShouldFire {
		if result.PermissionDenied {
			if _, err := m.repo.DeleteVehicleSubscription(ctx, wh.Trigger.ID, vehicleDID); err != nil {
				return fmt.Errorf("failed to delete vehicle subscription: %w", err)
			}
			// Surgical local invalidation; no broadcast. A permission-denied
			// signal stream can fire this thousands of times per second on a
			// misconfigured developer and broadcasting each one would thrash
			// every replica. Other replicas catch up via the periodic poll or
			// the next permission-denied on their side.
			m.webhookCache.InvalidateVehicleTrigger(vehicleDID.String(), wh.Trigger.ID)
		}
		return nil
	}
	payload, err := build(wh.Trigger, eval)
	if err != nil {
		return fmt.Errorf("failed to create webhook payload: %w", err)
	}
	return m.handleTriggeredWebhook(ctx, wh.Trigger, raw, payload)
}
