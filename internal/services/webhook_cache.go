package services

import (
	"context"
	"errors"
	"fmt"
	"sync"

	"github.com/DIMO-Network/vehicle-triggers-api/internal/db/models"
	"github.com/rs/zerolog"
	"github.com/volatiletech/sqlboiler/v4/boil"
	"github.com/volatiletech/sqlboiler/v4/queries/qm"
	"github.com/volatiletech/sqlboiler/v4/types"
)

var errNoWebhookConfig = errors.New("no webhook configurations found in the database")

type Webhook struct {
	ID                      string
	URL                     string
	Condition               string
	CooldownPeriod          int
	MetricName              string
	DeveloperLicenseAddress []byte
}

// WebhookCache is an in-memory map: vehicleTokenID -> telemetry identifier -> []Webhook.
type WebhookCache struct {
	mu       sync.RWMutex
	webhooks map[uint32]map[string][]Webhook
	logger   *zerolog.Logger
}

func NewWebhookCache(logger *zerolog.Logger) *WebhookCache {
	return &WebhookCache{
		webhooks: make(map[uint32]map[string][]Webhook),
		logger:   logger,
	}
}

var FetchWebhooksFromDBFunc = fetchEventVehicleWebhooks

// PopulateCache builds the cache from the database
func (wc *WebhookCache) PopulateCache(ctx context.Context, exec boil.ContextExecutor) error {
	webhooks, err := FetchWebhooksFromDBFunc(ctx, exec)
	wc.logger.Debug().
		Int("vehicles_with_hooks", len(webhooks)).
		Msg("Fetched raw hook map from DB")
	if err != nil {
		if errors.Is(err, errNoWebhookConfig) {
			webhooks = make(map[uint32]map[string][]Webhook)
		} else {
			return err
		}
	}

	wc.Update(webhooks)
	return nil
}

func (wc *WebhookCache) GetWebhooks(vehicleTokenID uint32, telemetry string) []Webhook {
	wc.mu.RLock()
	defer wc.mu.RUnlock()

	byVehicle, exists := wc.webhooks[vehicleTokenID]
	if !exists {
		wc.logger.Debug().
			Uint32("vehicle_token", vehicleTokenID).
			Msg("No webhooks cached for this vehicle")
		return nil
	}

	// log the list of available keys right before we try our lookup
	available := make([]string, 0, len(byVehicle))
	for k := range byVehicle {
		available = append(available, k)
	}
	wc.logger.Debug().
		Uint32("vehicle_token", vehicleTokenID).
		Str("looking_for", telemetry).
		Strs("available_keys", available).
		Msg("Cache lookup")

	return byVehicle[telemetry]
}

func (wc *WebhookCache) Update(newData map[uint32]map[string][]Webhook) {
	wc.mu.Lock()
	defer wc.mu.Unlock()
	wc.webhooks = newData
	var total int
	for _, m := range newData {
		for _, hooks := range m {
			total += len(hooks)
		}
	}
	wc.logger.Info().
		Int("webhook_config_count", total).
		Msg("Webhook cache updated")

}

// fetchEventVehicleWebhooks queries the EventVehicles table (with joined Event) and builds the cache
// It uses Event.Data as the telemetry identifier
func fetchEventVehicleWebhooks(ctx context.Context, exec boil.ContextExecutor) (map[uint32]map[string][]Webhook, error) {
	subs, err := models.VehicleSubscriptions(
		qm.Load(models.VehicleSubscriptionRels.Trigger),
	).All(ctx, exec)
	if err != nil {
		return nil, fmt.Errorf("failed to load EventVehicles: %w", err)
	}

	newData := make(map[uint32]map[string][]Webhook)
	for _, evv := range subs {
		vehicleTokenID, err := decimalToUint32(evv.VehicleTokenID)
		if err != nil {
			return nil, fmt.Errorf("converting token_id '%s': %w", evv.VehicleTokenID.String(), err)
		}
		event, err := models.FindTrigger(ctx, exec, evv.TriggerID)
		if err != nil {
			return nil, fmt.Errorf("failed to find event: %w", err)
		}

		if event.MetricName == "" {
			return nil, fmt.Errorf("event.MetricName empty for event %s (vehicle_token_id %d)", event.ID, vehicleTokenID)
		}
		if newData[vehicleTokenID] == nil {
			newData[vehicleTokenID] = make(map[string][]Webhook)
		}
		wh := Webhook{
			ID:                      event.ID,
			URL:                     event.TargetURI,
			Condition:               event.Condition,
			CooldownPeriod:          event.CooldownPeriod,
			MetricName:              event.MetricName,
			DeveloperLicenseAddress: event.DeveloperLicenseAddress,
		}

		newData[vehicleTokenID][event.MetricName] = append(newData[vehicleTokenID][event.MetricName], wh)
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
