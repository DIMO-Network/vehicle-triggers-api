package services

import (
	"context"
	"errors"
	"fmt"
	"strconv"
	"strings"
	"sync"

	"github.com/DIMO-Network/vehicle-triggers-api/internal/db/models"
	"github.com/DIMO-Network/vehicle-triggers-api/internal/utils"
	"github.com/rs/zerolog"
	"github.com/volatiletech/sqlboiler/v4/boil"
	"github.com/volatiletech/sqlboiler/v4/queries/qm"
	"github.com/volatiletech/sqlboiler/v4/types"
)

type Webhook struct {
	ID                      string
	URL                     string
	Trigger                 string
	CooldownPeriod          int
	Data                    string
	Setup                   string
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
	rawData, err := FetchWebhooksFromDBFunc(ctx, exec)
	wc.logger.Debug().
		Int("vehicles_with_hooks", len(rawData)).
		Msg("Fetched raw hook map from DB")
	for veh, byName := range rawData {
		for name, hooks := range byName {
			wc.logger.Debug().
				Uint32("vehicle", veh).
				Str("raw_data_field", name).
				Int("hook_count", len(hooks)).
				Msg("  ↳ DB row")
		}
	}

	if err != nil {
		if err.Error() == "no webhook configurations found in the database" {
			rawData = make(map[uint32]map[string][]Webhook)
		} else {
			return err
		}
	}

	// normalize keys
	normalized := make(map[uint32]map[string][]Webhook)
	for tokenID, hooksByRaw := range rawData {
		normalized[tokenID] = make(map[string][]Webhook)
		for rawName, hooks := range hooksByRaw {
			normKey := utils.NormalizeSignalName(rawName)
			// update Data field to normalized name as well
			for i := range hooks {
				hooks[i].Data = normKey
			}
			normalized[tokenID][normKey] = append(normalized[tokenID][normKey], hooks...)
		}
	}

	wc.Update(normalized)
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
	// additional logging for debug
	for tokenID, bySignal := range newData {
		keys := make([]string, 0, len(bySignal))
		for signal := range bySignal {
			keys = append(keys, signal)
		}
		wc.logger.Debug().
			Uint32("vehicle_token", tokenID).
			Strs("signals", keys).
			Msg("Cached signals for vehicle")
	}
}

// fetchEventVehicleWebhooks queries the EventVehicles table (with joined Event) and builds the cache
// It uses Event.Data as the telemetry identifier
func fetchEventVehicleWebhooks(ctx context.Context, exec boil.ContextExecutor) (map[uint32]map[string][]Webhook, error) {
	evVehicles, err := models.EventVehicles(
		qm.Load(models.EventVehicleRels.Event),
	).All(ctx, exec)
	if err != nil {
		return nil, err
	}

	newData := make(map[uint32]map[string][]Webhook)
	for _, evv := range evVehicles {
		vehicleTokenID, err := decimalToUint32(evv.VehicleTokenID)
		if err != nil {
			return nil, fmt.Errorf("converting token_id: %s", evv.VehicleTokenID.String())
		}
		event, err := models.FindEvent(ctx, exec, evv.EventID)
		if err != nil {
			return nil, fmt.Errorf("FindEvent failed: %s", evv.EventID)
		}

		raw := strings.TrimSpace(event.Data)
		if raw == "" {
			return nil, fmt.Errorf("event.Data empty for event %s (vehicle_token_id %d)", event.ID, vehicleTokenID)
		}
		telemetry := utils.NormalizeSignalName(raw)
		if newData[vehicleTokenID] == nil {
			newData[vehicleTokenID] = make(map[string][]Webhook)
		}
		wh := Webhook{
			ID:                      event.ID,
			URL:                     event.TargetURI,
			Trigger:                 event.Trigger,
			CooldownPeriod:          event.CooldownPeriod,
			Data:                    telemetry,
			Setup:                   event.Setup,
			DeveloperLicenseAddress: evv.DeveloperLicenseAddress,
		}

		newData[vehicleTokenID][telemetry] = append(newData[vehicleTokenID][telemetry], wh)
	}
	if len(newData) == 0 {
		return nil, errors.New("no webhook configurations found in the database")
	}
	return newData, nil
}

func decimalToUint32(d types.Decimal) (uint32, error) {
	s := d.String()
	val, err := strconv.ParseUint(s, 10, 32)
	if err != nil {
		return 0, err
	}
	return uint32(val), nil
}
