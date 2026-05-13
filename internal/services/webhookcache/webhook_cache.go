package webhookcache

import (
	"context"
	"errors"
	"fmt"
	"runtime"
	"sync"
	"sync/atomic"
	"time"

	"github.com/DIMO-Network/vehicle-triggers-api/internal/celcondition"
	"github.com/DIMO-Network/vehicle-triggers-api/internal/config"
	"github.com/DIMO-Network/vehicle-triggers-api/internal/db/models"
	"github.com/DIMO-Network/vehicle-triggers-api/internal/services/triggersrepo"
	"github.com/DIMO-Network/vehicle-triggers-api/internal/signals"
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
	logger := zerolog.Ctx(ctx)
	start := time.Now()
	logMemStats(logger, "populate_cache_enter")

	webhooks, err := wc.fetchVehicleWebhooks(ctx)
	if err != nil {
		if errors.Is(err, errNoWebhookConfig) {
			webhooks = make(map[string]map[string][]*Webhook)
		} else {
			return err
		}
	}

	wc.Update(webhooks)
	logger.Info().
		Int("asset_count", len(webhooks)).
		Dur("elapsed", time.Since(start)).
		Msg("webhook cache populated")
	logMemStats(logger, "populate_cache_exit")
	return nil
}

// logMemStats records the current Go heap stats so we can attribute OOMs to a
// specific startup phase. Cheap: ReadMemStats stops the world briefly but is
// negligible at startup.
func logMemStats(logger *zerolog.Logger, phase string) {
	var m runtime.MemStats
	runtime.ReadMemStats(&m)
	logger.Info().
		Str("phase", phase).
		Uint64("heap_alloc_mb", m.HeapAlloc>>20).
		Uint64("heap_sys_mb", m.HeapSys>>20).
		Uint64("heap_inuse_mb", m.HeapInuse>>20).
		Uint64("heap_objects", m.HeapObjects).
		Uint64("sys_mb", m.Sys>>20).
		Uint32("num_gc", m.NumGC).
		Msg("memstats")
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

// GetWebhooks returns the webhooks for a given vehicle token id, service, and metric name
// Do not modify the returned slice or the webhooks themselves since they are shared with other callers.
func (wc *WebhookCache) GetWebhooks(assetDID string, service, metricName string) []*Webhook {
	wc.mu.RLock()
	defer wc.mu.RUnlock()

	byVehicle, exists := wc.webhooks[assetDID]
	if !exists {
		return nil
	}
	key := webhookKey(service, metricName)

	return byVehicle[key]
}

func (wc *WebhookCache) Update(newData map[string]map[string][]*Webhook) {
	wc.mu.Lock()
	defer wc.mu.Unlock()
	wc.webhooks = newData
	wc.lastRefresh = time.Now()
}

func (wc *WebhookCache) fetchVehicleWebhooks(ctx context.Context) (map[string]map[string][]*Webhook, error) {
	logger := zerolog.Ctx(ctx)
	fetchStart := time.Now()
	subs, err := wc.repo.InternalGetAllVehicleSubscriptions(ctx)
	if err != nil {
		return nil, fmt.Errorf("failed to get all vehicle subscriptions: %w", err)
	}
	logger.Info().
		Int("sub_count", len(subs)).
		Dur("fetch_elapsed", time.Since(fetchStart)).
		Msg("loaded vehicle subscriptions")
	logMemStats(logger, "after_load_subs")

	// There will be many more vehicle subscriptions than triggers,
	// so we can share the same trigger object to reduce memory usage and database calls
	uniqueTriggers := make(map[string]*Webhook)

	newData := make(map[string]map[string][]*Webhook)
	triggerFetchStart := time.Now()
	triggerFetches := 0
	for _, sub := range subs {
		webhook, ok := uniqueTriggers[sub.TriggerID]
		if !ok {
			webhook = &Webhook{}
			triggerFetches++
			webhook.Trigger, err = wc.repo.InternalGetTriggerByID(ctx, sub.TriggerID)
			if err != nil {
				logger.Error().Err(err).Msg("failed to get trigger by id for webhook cache")
				continue
			}
			valueType := ""
			if triggersrepo.IsSignalService(webhook.Trigger.Service) {
				valueType = signals.GetSignalDefinitionOrDefault(signals.BareSignalName(webhook.Trigger.MetricName), signals.NumberType).ValueType
			}
			webhook.Program, err = celcondition.PrepareCondition(webhook.Trigger.Service, webhook.Trigger.Condition, valueType)
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

		key := webhookKey(webhook.Trigger.Service, webhook.Trigger.MetricName)
		newData[sub.AssetDid][key] = append(newData[sub.AssetDid][key], webhook)
	}
	logger.Info().
		Int("sub_count", len(subs)).
		Int("unique_trigger_fetches", triggerFetches).
		Int("unique_trigger_cache", len(uniqueTriggers)).
		Int("asset_count", len(newData)).
		Dur("build_elapsed", time.Since(triggerFetchStart)).
		Msg("webhook cache build complete")
	logMemStats(logger, "after_build_cache")

	if len(newData) == 0 {
		return nil, errNoWebhookConfig
	}
	return newData, nil
}

func webhookKey(service, metricName string) string {
	return service + ":" + metricName
}
