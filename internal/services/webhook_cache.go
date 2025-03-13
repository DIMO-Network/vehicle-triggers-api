package services

import (
	"context"
	"encoding/json"
	"errors"
	"github.com/DIMO-Network/vehicle-events-api/internal/db/models"
	"github.com/volatiletech/sqlboiler/v4/boil"
	"github.com/volatiletech/sqlboiler/v4/queries/qm"
	"sync"
)

var FetchWebhooksFromDBFunc = fetchWebhooksFromDB

// Webhook represents a single webhook registration for a particular tokenId and signal name
type Webhook struct {
	URL       string
	Condition string
}
type WebhookParameters struct {
	TokenID    uint32 `json:"tokenId"`
	SignalName string `json:"signalName"`
}

// WebhookCache is an in-memory map of: tokenId -> map[signalName] -> []Webhook
type WebhookCache struct {
	mu       sync.RWMutex
	webhooks map[uint32]map[string][]Webhook
}

// NewWebhookCache returns a new, empty cache.
// In production call PopulateCache() at startup to load initial data
func NewWebhookCache() *WebhookCache {
	return &WebhookCache{
		webhooks: make(map[uint32]map[string][]Webhook),
	}
}

func (wc *WebhookCache) PopulateCache(ctx context.Context, exec boil.ContextExecutor) error {
	newData, err := FetchWebhooksFromDBFunc(ctx, exec)
	if err != nil {
		return err
	}
	wc.Update(newData)
	return nil
}

// GetWebhooks returns the list of webhooks for a given tokenId and signal name.
func (wc *WebhookCache) GetWebhooks(tokenId uint32, signalName string) []Webhook {
	wc.mu.RLock()
	defer wc.mu.RUnlock()

	byToken, ok := wc.webhooks[tokenId]
	if !ok {
		return nil
	}
	return byToken[signalName]
}

func (wc *WebhookCache) SetWebhooks(tokenId uint32, signalName string, hooks []Webhook) {
	wc.mu.Lock()
	defer wc.mu.Unlock()

	if wc.webhooks == nil {
		wc.webhooks = make(map[uint32]map[string][]Webhook)
	}
	if wc.webhooks[tokenId] == nil {
		wc.webhooks[tokenId] = make(map[string][]Webhook)
	}
	wc.webhooks[tokenId][signalName] = hooks
}

// Update replaces the entire webhook cache with newData.
func (wc *WebhookCache) Update(newData map[uint32]map[string][]Webhook) {
	wc.mu.Lock()
	defer wc.mu.Unlock()
	wc.webhooks = newData
}

// fetchWebhooksFromDB performs an actual DB query
// from the events table it returns a nested map keyed by tokenId and signalName
func fetchWebhooksFromDB(ctx context.Context, exec boil.ContextExecutor) (map[uint32]map[string][]Webhook, error) {
	events, err := models.Events(
		qm.OrderBy("id"),
	).All(ctx, exec)
	if err != nil {
		return nil, err
	}

	newData := make(map[uint32]map[string][]Webhook)
	for _, ev := range events {
		if !ev.Parameters.Valid {
			continue
		}

		var params WebhookParameters
		b, err := ev.Parameters.MarshalJSON()
		if err != nil {
			continue
		}
		if err := json.Unmarshal(b, &params); err != nil {
			continue
		}
		if params.TokenID == 0 || params.SignalName == "" {
			continue
		}

		if newData[params.TokenID] == nil {
			newData[params.TokenID] = make(map[string][]Webhook)
		}

		webhook := Webhook{
			URL:       ev.TargetURI,
			Condition: ev.Trigger,
		}
		newData[params.TokenID][params.SignalName] = append(newData[params.TokenID][params.SignalName], webhook)
	}

	if len(newData) == 0 {
		return nil, errors.New("no webhook configurations found in the database")
	}
	return newData, nil
}
