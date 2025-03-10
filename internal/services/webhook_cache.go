package services

import (
	"sync"
)

// Webhook represents a single webhook registration for a particular tokenId and signal name
type Webhook struct {
	URL       string
	Condition string
}

// WebhookCache is an in-memory map of: tokenId -> map[signalName] -> []Webhook
type WebhookCache struct {
	mu       sync.RWMutex
	webhooks map[uint32]map[string][]Webhook
}

// NewWebhookCache returns a cache with some demo data. TODO: what will the real data look like?
func NewWebhookCache() *WebhookCache {
	// todo: call populate cache
	return &WebhookCache{
		webhooks: map[uint32]map[string][]Webhook{
			// Example
			123: {
				"odometer": {
					{
						URL:       "http://localhost:8081/webhook",
						Condition: "valueNumber > 100", // Only fire if > 100
					},
				},
			},
		},
	}
}

// todo: add new function that populates cache from the database
func (wc *WebhookCache) PopulateCache() {
	// mutex
	// connect to db table
	// clear the cache
	// iterate
	// put stuff in the cache
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
