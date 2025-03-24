package services

import (
	"context"
	"errors"
	"strconv"
	"strings"
	"sync"

	"github.com/DIMO-Network/vehicle-events-api/internal/db/models"
	"github.com/volatiletech/sqlboiler/v4/boil"
	"github.com/volatiletech/sqlboiler/v4/queries/qm"
	"github.com/volatiletech/sqlboiler/v4/types"
)

// Webhook represents a webhook configuration.
type Webhook struct {
	ID             string
	URL            string
	Trigger        string
	CooldownPeriod int
	Data           string
}

// WebhookCache is an in-memory map: vehicleTokenID -> telemetry identifier -> []Webhook.
type WebhookCache struct {
	mu       sync.RWMutex
	webhooks map[uint32]map[string][]Webhook
}

func NewWebhookCache() *WebhookCache {
	return &WebhookCache{
		webhooks: make(map[uint32]map[string][]Webhook),
	}
}

var FetchWebhooksFromDBFunc = fetchEventVehicleWebhooks

// PopulateCache builds the cache from the database
func (wc *WebhookCache) PopulateCache(ctx context.Context, exec boil.ContextExecutor) error {
	newData, err := FetchWebhooksFromDBFunc(ctx, exec)
	if err != nil {
		return err
	}
	wc.Update(newData)
	return nil
}

func (wc *WebhookCache) GetWebhooks(vehicleTokenID uint32, telemetry string) []Webhook {
	wc.mu.RLock()
	defer wc.mu.RUnlock()
	if byVehicle, ok := wc.webhooks[vehicleTokenID]; ok {
		return byVehicle[telemetry]
	}
	return nil
}

func (wc *WebhookCache) Update(newData map[uint32]map[string][]Webhook) {
	wc.mu.Lock()
	defer wc.mu.Unlock()
	wc.webhooks = newData
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
			continue
		}
		if evv.R == nil || evv.R.Event == nil {
			continue
		}
		event := evv.R.Event
		telemetry := strings.TrimSpace(event.Data)
		if telemetry == "" {
			continue
		}
		if newData[vehicleTokenID] == nil {
			newData[vehicleTokenID] = make(map[string][]Webhook)
		}
		wh := Webhook{
			ID:             event.ID,
			URL:            event.TargetURI,
			Trigger:        event.Trigger,
			CooldownPeriod: event.CooldownPeriod,
			Data:           telemetry,
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
