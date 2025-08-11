package webhookcache

import (
	"context"
	"errors"
	"fmt"
	"sync"
	"sync/atomic"
	"time"

	"github.com/DIMO-Network/vehicle-triggers-api/internal/celcondition"
	"github.com/DIMO-Network/vehicle-triggers-api/internal/db/models"
	"github.com/DIMO-Network/vehicle-triggers-api/internal/services/triggersrepo"
	"github.com/google/cel-go/cel"
	"github.com/rs/zerolog"
	"github.com/volatiletech/sqlboiler/v4/types"
)

var errNoWebhookConfig = errors.New("no webhook configurations found in the database")

type Webhook struct {
	Trigger *models.Trigger
	Program cel.Program
}

type Repository interface {
	InternalGetAllVehicleSubscriptions(ctx context.Context) ([]*models.VehicleSubscription, error)
	InternalGetTriggerByID(ctx context.Context, triggerID string) (*models.Trigger, error)
}

// WebhookCache is an in-memory map: vehicleTokenID -> signal name -> []*models.Trigger.
type WebhookCache struct {
	mu          sync.RWMutex
	webhooks    map[uint32]map[string][]*Webhook
	repo        Repository
	lastRefresh time.Time // last time the cache was refreshed
	schedule    atomic.Bool
}

func NewWebhookCache(repo Repository) *WebhookCache {
	return &WebhookCache{
		webhooks: make(map[uint32]map[string][]*Webhook),
		repo:     repo,
	}
}

// PopulateCache builds the cache from the database
func (wc *WebhookCache) PopulateCache(ctx context.Context) error {
	webhooks, err := wc.fetchEventVehicleWebhooks(ctx)
	if err != nil {
		if errors.Is(err, errNoWebhookConfig) {
			webhooks = make(map[uint32]map[string][]*Webhook)
		} else {
			return err
		}
	}

	wc.Update(webhooks)
	return nil
}

func (wc *WebhookCache) ScheduleRefresh(ctx context.Context) {
	wc.schedule.Store(true)
	go func() {
		time.Sleep(time.Second * 5)
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

func (wc *WebhookCache) GetWebhooks(vehicleTokenID uint32, telemetry string) []*Webhook {
	wc.mu.RLock()
	defer wc.mu.RUnlock()

	byVehicle, exists := wc.webhooks[vehicleTokenID]
	if !exists {
		return nil
	}

	return byVehicle[telemetry]
}

func (wc *WebhookCache) Update(newData map[uint32]map[string][]*Webhook) {
	wc.mu.Lock()
	defer wc.mu.Unlock()
	wc.webhooks = newData
	wc.lastRefresh = time.Now()
}

func (wc *WebhookCache) fetchEventVehicleWebhooks(ctx context.Context) (map[uint32]map[string][]*Webhook, error) {
	subs, err := wc.repo.InternalGetAllVehicleSubscriptions(ctx)
	if err != nil {
		return nil, fmt.Errorf("failed to get all vehicle subscriptions: %w", err)
	}

	// There will be many more vehicle subscriptions than triggers,
	// so we can share the same trigger object to reduce memory usage and database calls
	uniqueTriggers := make(map[string]*Webhook)

	newData := make(map[uint32]map[string][]*Webhook)
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
		vehicleTokenID, err := decimalToUint32(sub.VehicleTokenID)
		if err != nil {
			logger.Error().Err(err).Str("trigger_id", webhook.Trigger.ID).Str("vehicle_token_id", sub.VehicleTokenID.String()).Msg("failed to convert token_id")
			continue
		}

		if newData[vehicleTokenID] == nil {
			newData[vehicleTokenID] = make(map[string][]*Webhook)
		}

		newData[vehicleTokenID][webhook.Trigger.MetricName] = append(newData[vehicleTokenID][webhook.Trigger.MetricName], webhook)
	}
	if len(newData) == 0 {
		return nil, errNoWebhookConfig
	}
	return newData, nil
}

func decimalToUint32(d types.Decimal) (uint32, error) {
	val, ok := d.Uint64()
	if !ok {
		return 0, fmt.Errorf("failed to convert decimal to uint64")
	}
	return uint32(val), nil
}
