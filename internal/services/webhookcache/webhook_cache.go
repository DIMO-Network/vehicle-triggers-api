package webhookcache

import (
	"context"
	"errors"
	"fmt"
	"sync"
	"sync/atomic"
	"time"

	"github.com/DIMO-Network/vehicle-triggers-api/internal/celcondition"
	"github.com/DIMO-Network/vehicle-triggers-api/internal/config"
	"github.com/DIMO-Network/vehicle-triggers-api/internal/db/models"
	"github.com/DIMO-Network/vehicle-triggers-api/internal/services/triggersrepo"
	"github.com/google/cel-go/cel"
	"github.com/rs/zerolog"
)

var errNoWebhookConfig = errors.New("no webhook configurations found in the database")

const defaultCacheDebounceTime = 5 * time.Second

type Webhook struct {
	Trigger *models.Trigger
	Program cel.Program
}

type Repository interface {
	InternalGetAllVehicleSubscriptions(ctx context.Context) ([]*models.VehicleSubscription, error)
	InternalGetTriggerByID(ctx context.Context, triggerID string) (*models.Trigger, error)
}

// WebhookCache is an in-memory map: assetDID -> signal name -> []*models.Trigger.
type WebhookCache struct {
	mu          sync.RWMutex
	webhooks    map[string]map[string][]*Webhook
	repo        Repository
	lastRefresh time.Time // last time the cache was refreshed
	schedule    atomic.Bool
	debounce    time.Duration
}

func NewWebhookCache(repo Repository, settings *config.Settings) *WebhookCache {
	debounce := settings.CacheDebounceTime
	if debounce == 0 {
		debounce = defaultCacheDebounceTime
	}
	return &WebhookCache{
		webhooks: make(map[string]map[string][]*Webhook),
		repo:     repo,
		debounce: debounce,
	}
}

// PopulateCache builds the cache from the database
func (wc *WebhookCache) PopulateCache(ctx context.Context) error {
	webhooks, err := wc.fetchEventVehicleWebhooks(ctx)
	if err != nil {
		if errors.Is(err, errNoWebhookConfig) {
			webhooks = make(map[string]map[string][]*Webhook)
		} else {
			return err
		}
	}

	wc.Update(webhooks)
	return nil
}

func (wc *WebhookCache) ScheduleRefresh(ctx context.Context) {
	if wc.schedule.CompareAndSwap(false, true) {
		go func() {
			time.Sleep(wc.debounce)
			if wc.schedule.Load() {
				// if we waited and we still want to refresh, do it
				wc.schedule.Store(false)
				err := wc.PopulateCache(ctx)
				if err != nil {
					zerolog.Ctx(ctx).Error().Err(err).Msg("failed to populate webhook cache")
				}
			}
		}()
	}
}

func (wc *WebhookCache) GetWebhooks(assetDID string, telemetry string) []*Webhook {
	wc.mu.RLock()
	defer wc.mu.RUnlock()

	byVehicle, exists := wc.webhooks[assetDID]
	if !exists {
		return nil
	}

	return byVehicle[telemetry]
}

func (wc *WebhookCache) Update(newData map[string]map[string][]*Webhook) {
	wc.mu.Lock()
	defer wc.mu.Unlock()
	wc.webhooks = newData
	wc.lastRefresh = time.Now()
}

func (wc *WebhookCache) fetchEventVehicleWebhooks(ctx context.Context) (map[string]map[string][]*Webhook, error) {
	subs, err := wc.repo.InternalGetAllVehicleSubscriptions(ctx)
	if err != nil {
		return nil, fmt.Errorf("failed to get all vehicle subscriptions: %w", err)
	}

	// There will be many more vehicle subscriptions than triggers,
	// so we can share the same trigger object to reduce memory usage and database calls
	uniqueTriggers := make(map[string]*Webhook)

	newData := make(map[string]map[string][]*Webhook)
	logger := zerolog.Ctx(ctx)
	for _, sub := range subs {
		webhook, ok := uniqueTriggers[sub.TriggerID]
		if !ok {
			webhook = &Webhook{}
			webhook.Trigger, err = wc.repo.InternalGetTriggerByID(ctx, sub.TriggerID)
			if err != nil {
				logger.Error().Err(err).Msg("failed to get trigger by id for webhook cache")
				continue
			}
			webhook.Program, err = celcondition.PrepareCondition(webhook.Trigger.Condition)
			if err != nil {
				logger.Error().Err(err).Str("trigger_id", webhook.Trigger.ID).Msg("failed to prepare condition")
				continue
			}
			uniqueTriggers[sub.TriggerID] = webhook
		}
		if webhook.Trigger.Status != triggersrepo.StatusEnabled {
			continue
		}

		if newData[sub.AssetDid] == nil {
			newData[sub.AssetDid] = make(map[string][]*Webhook)
		}

		newData[sub.AssetDid][webhook.Trigger.MetricName] = append(newData[sub.AssetDid][webhook.Trigger.MetricName], webhook)
	}
	if len(newData) == 0 {
		return nil, errNoWebhookConfig
	}
	return newData, nil
}
