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

// WebhookCache is an in-memory map: assetDID -> signal name -> []*Webhook.
//
// Two levels of caching live here:
//   - `webhooks`: the per-vehicle dispatch index hit on every signal.
//   - `compiled`: a shared cache of {trigger row + compiled CEL program}
//     keyed by trigger ID. Rebuilds reuse compiled programs for triggers
//     whose config hasn't changed, so a 5-min periodic refresh on a
//     stable config does ~0 CEL compiles. CRUD invalidations drop the
//     affected trigger from `compiled` so the next rebuild recompiles it.
//
// This is the "diff-rebuild" half of the V2 review. Full tenant-scoped
// lazy loading (per-developer subscriptions fetched on first signal)
// is deliberately not implemented yet - the compiled-program cache is
// the easy 80% of the CPU win and avoids the cache-coherence complexity
// of partial scoping.
type WebhookCache struct {
	mu          sync.RWMutex
	webhooks    map[string]map[string][]*Webhook
	repo        Repository
	lastRefresh time.Time // last time the cache was refreshed
	schedule    atomic.Bool
	debounce    time.Duration
	buildWorkers int
	notifier    Notifier

	// compiled is the shared {trigger row, CEL program} cache keyed by
	// trigger ID. Owned by mu (read under RLock for build-time lookup,
	// write under Lock for refresh + targeted invalidation). nil program
	// entries are never stored - compile failures are skipped at fetch
	// time. The dispatch path doesn't read this map; it only reads the
	// `webhooks` map populated from it.
	compiled map[string]*Webhook
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
		compiled:     make(map[string]*Webhook),
		repo:         repo,
		debounce:     debounce,
		buildWorkers: workers,
		notifier:     noopNotifier{},
	}
}

// InvalidateTrigger drops a single trigger from the compiled-program cache
// so the next rebuild re-fetches and recompiles it. Called by the
// cachebroadcast subscriber when a remote replica's CRUD broadcast carries
// a specific webhook ID. Targeted invalidation lets a CRUD on one trigger
// avoid recompiling every other trigger's CEL program on every replica.
//
// Does NOT trigger an immediate rebuild - the caller decides cadence via
// ScheduleRefreshSilent. Keep these methods orthogonal so a burst of
// CRUDs collapses into one rebuild after the debounce window.
func (wc *WebhookCache) InvalidateTrigger(triggerID string) {
	if triggerID == "" {
		return
	}
	wc.mu.Lock()
	delete(wc.compiled, triggerID)
	wc.mu.Unlock()
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

	// Diff-rebuild: split the set into already-compiled (cheap, reused)
	// and new (must fetch + compile). Eviction of compiled-but-no-longer-
	// subscribed entries is done at the end so we keep CPU bounded by
	// "what actually changed since the last refresh."
	triggerFetchStart := time.Now()
	uniqueTriggers, reusedCount := wc.buildCompiledIndex(ctx, uniqueTriggerIDs)

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
		Int("reused_compiled", reusedCount).
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

// buildCompiledIndex returns the full {triggerID -> *Webhook} index for the
// requested IDs, reusing entries from wc.compiled when present and only
// fetching + CEL-compiling the new ones. Returns (index, reusedCount).
//
// Eviction: triggers that fall out of triggerIDs (no remaining subscribers)
// are dropped from wc.compiled at the end so the map can't grow unbounded
// as customers churn triggers.
func (wc *WebhookCache) buildCompiledIndex(ctx context.Context, triggerIDs map[string]struct{}) (map[string]*Webhook, int) {
	wc.mu.RLock()
	out := make(map[string]*Webhook, len(triggerIDs))
	missing := make(map[string]struct{}, len(triggerIDs))
	for id := range triggerIDs {
		if w, ok := wc.compiled[id]; ok {
			out[id] = w
			continue
		}
		missing[id] = struct{}{}
	}
	wc.mu.RUnlock()
	reused := len(out)

	if len(missing) > 0 {
		fresh := wc.compileTriggersParallel(ctx, missing)
		wc.mu.Lock()
		for id, w := range fresh {
			wc.compiled[id] = w
			out[id] = w
		}
		// Evict compiled entries that no longer have any subscribers so
		// the map stays bounded as triggers churn. Comparing against
		// triggerIDs (the *current* subscription set) is the right
		// invariant - anything not in there isn't being dispatched on.
		for id := range wc.compiled {
			if _, ok := triggerIDs[id]; !ok {
				delete(wc.compiled, id)
			}
		}
		wc.mu.Unlock()
	}
	return out, reused
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
