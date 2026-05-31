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

const (
	defaultCacheDebounceTime = 5 * time.Second
	defaultCacheBuildWorkers = 2
)

type Webhook struct {
	Trigger *models.Trigger
	Program cel.Program
}

type Repository interface {
	InternalGetAllVehicleSubscriptions(ctx context.Context) ([]*models.VehicleSubscription, error)
	InternalGetTriggerByID(ctx context.Context, triggerID string) (*models.Trigger, error)
	DecryptSigningSecret(stored string) (string, error)
}

// Notifier broadcasts a "config changed" event over NATS so other replicas
// can invalidate their own caches in milliseconds instead of waiting for
// the periodic poll. Set via SetNotifier when NATS is configured.
type Notifier interface {
	Notify(ctx context.Context, webhookID string, op string) error
}

type noopNotifier struct{}

func (noopNotifier) Notify(context.Context, string, string) error { return nil }

// WebhookCache is an in-memory map: assetDID -> signal name -> []*models.Trigger.
type WebhookCache struct {
	mu            sync.RWMutex
	webhooks      map[string]map[string][]*Webhook
	repo          Repository
	lastRefresh   time.Time // last time the cache was refreshed
	schedule      atomic.Bool
	debounce      time.Duration
	buildWorkers  int
	notifier      Notifier
}

func NewWebhookCache(repo Repository, settings *config.Settings) *WebhookCache {
	debounce := settings.Cache.DebounceTime
	if debounce == 0 {
		debounce = defaultCacheDebounceTime
	}
	workers := settings.Cache.BuildWorkers
	if workers < 1 {
		workers = defaultCacheBuildWorkers
	}
	return &WebhookCache{
		webhooks:     make(map[string]map[string][]*Webhook),
		repo:         repo,
		debounce:     debounce,
		buildWorkers: workers,
		notifier:     noopNotifier{},
	}
}

// SetNotifier wires the cache to publish a "config changed" event on
// ScheduleRefresh. Used by the API CRUD paths; the subscriber that receives
// remote notifications calls ScheduleRefreshSilent instead so the
// invalidation event doesn't echo back into another publish.
func (wc *WebhookCache) SetNotifier(n Notifier) {
	if n == nil {
		n = noopNotifier{}
	}
	wc.notifier = n
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

// ScheduleRefresh debounces a rebuild of this replica's cache and publishes
// a cross-replica invalidation event. Called from CRUD handlers.
func (wc *WebhookCache) ScheduleRefresh(ctx context.Context) {
	wc.scheduleRefresh(ctx, true)
}

// ScheduleRefreshSilent is the receiver side of cross-replica invalidation:
// rebuilds the local cache but does NOT re-broadcast, so notifications
// don't echo into infinite republishing across replicas.
func (wc *WebhookCache) ScheduleRefreshSilent(ctx context.Context) {
	wc.scheduleRefresh(ctx, false)
}

func (wc *WebhookCache) scheduleRefresh(ctx context.Context, broadcast bool) {
	if broadcast && wc.notifier != nil {
		if err := wc.notifier.Notify(ctx, "", "any"); err != nil {
			zerolog.Ctx(ctx).Warn().Err(err).Msg("cache invalidate publish failed; relying on poll")
		}
	}
	if wc.schedule.CompareAndSwap(false, true) {
		// Capture the logger from the caller's ctx, but detach the value
		// chain from the request-bound ctx itself. Fiber pools fasthttp
		// request contexts; holding one across the debounce + DB roundtrip
		// races with the next request reusing the same memory. Use a fresh
		// background ctx for the actual refresh and copy only the logger so
		// log lines still get the right structured fields.
		logger := zerolog.Ctx(ctx)
		go func() {
			time.Sleep(wc.debounce)
			if wc.schedule.Load() {
				wc.schedule.Store(false)
				bgCtx := logger.WithContext(context.Background())
				if err := wc.PopulateCache(bgCtx); err != nil {
					logger.Error().Err(err).Msg("failed to populate webhook cache")
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

// InvalidateVehicleTrigger removes the cached webhook entries for the given
// (assetDID, triggerID) pair without broadcasting a cross-replica refresh.
// Use this on the hot path - for example when eval discovers a revoked
// permission and unsubscribes the vehicle locally. The 5-minute periodic
// refresh is the reconciliation safety net for other replicas; we
// deliberately do NOT publish a cachebroadcast event here because
// permission-denied is a per-signal event and broadcasting it would
// stampede every replica thousands of times per second on a misconfigured
// developer.
func (wc *WebhookCache) InvalidateVehicleTrigger(assetDID, triggerID string) {
	wc.mu.Lock()
	defer wc.mu.Unlock()

	byVehicle, ok := wc.webhooks[assetDID]
	if !ok {
		return
	}
	for key, hooks := range byVehicle {
		filtered := hooks[:0]
		for _, h := range hooks {
			if h.Trigger == nil || h.Trigger.ID != triggerID {
				filtered = append(filtered, h)
			}
		}
		if len(filtered) == 0 {
			delete(byVehicle, key)
		} else {
			byVehicle[key] = filtered
		}
	}
	if len(byVehicle) == 0 {
		delete(wc.webhooks, assetDID)
	}
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

	// Collect unique trigger IDs first; many subscriptions share triggers.
	uniqueTriggerIDs := make(map[string]struct{}, len(subs))
	for _, sub := range subs {
		uniqueTriggerIDs[sub.TriggerID] = struct{}{}
	}

	// Fetch + CEL-compile each unique trigger in parallel. Each compile is
	// CPU-bound (~5ms) and the loop is hot on startup (~10k triggers => 50s
	// serial). Parallelising across GOMAXPROCS workers brings build_elapsed
	// well under the kubelet liveness deadline.
	triggerFetchStart := time.Now()
	uniqueTriggers := wc.compileTriggersParallel(ctx, uniqueTriggerIDs)

	newData := make(map[string]map[string][]*Webhook)
	for _, sub := range subs {
		webhook, ok := uniqueTriggers[sub.TriggerID]
		if !ok {
			continue
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
		Int("unique_trigger_ids", len(uniqueTriggerIDs)).
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

// compileTriggersParallel fetches and CEL-compiles each unique trigger in
// parallel. Worker count comes from CACHE_BUILD_WORKERS so prod can tune it
// against its CPU limit and DB connection pool. Triggers that fail to fetch
// or compile are logged and skipped, mirroring the previous behaviour of
// the serial loop.
func (wc *WebhookCache) compileTriggersParallel(ctx context.Context, triggerIDs map[string]struct{}) map[string]*Webhook {
	logger := zerolog.Ctx(ctx)

	workers := wc.buildWorkers
	if workers < 1 {
		workers = defaultCacheBuildWorkers
	}

	type result struct {
		id      string
		webhook *Webhook
	}

	ids := make(chan string, len(triggerIDs))
	for id := range triggerIDs {
		ids <- id
	}
	close(ids)

	results := make(chan result, len(triggerIDs))
	var wg sync.WaitGroup
	wg.Add(workers)
	for w := 0; w < workers; w++ {
		go func() {
			defer wg.Done()
			for id := range ids {
				trigger, err := wc.repo.InternalGetTriggerByID(ctx, id)
				if err != nil {
					logger.Error().Err(err).Str("trigger_id", id).Msg("failed to get trigger by id for webhook cache")
					continue
				}
				// Decrypt the signing secret here so downstream code paths
				// (sender HMAC, audit) see plaintext. Failure to decrypt is
				// not fatal - the trigger still functions for non-signing
				// concerns; the sender just won't add a signature header.
				if trigger.SigningSecret.Valid {
					if pt, err := wc.repo.DecryptSigningSecret(trigger.SigningSecret.String); err == nil {
						trigger.SigningSecret.String = pt
					} else {
						logger.Warn().Err(err).Str("trigger_id", id).Msg("failed to decrypt signing secret; webhook will be sent unsigned")
						trigger.SigningSecret.Valid = false
					}
				}
				valueType := ""
				if triggersrepo.IsSignalService(trigger.Service) {
					valueType = signals.GetSignalDefinitionOrDefault(signals.BareSignalName(trigger.MetricName), signals.NumberType).ValueType
				}
				program, err := celcondition.PrepareCondition(trigger.Service, trigger.Condition, valueType)
				if err != nil {
					logger.Error().Err(err).Str("trigger_id", trigger.ID).Msg("failed to prepare condition")
					continue
				}
				results <- result{id: id, webhook: &Webhook{Trigger: trigger, Program: program}}
			}
		}()
	}

	go func() {
		wg.Wait()
		close(results)
	}()

	out := make(map[string]*Webhook, len(triggerIDs))
	for r := range results {
		out[r.id] = r.webhook
	}
	return out
}
