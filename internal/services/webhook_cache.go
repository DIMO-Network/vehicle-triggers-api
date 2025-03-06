package services

// Webhook represents a single webhook registration for a particular tokenId and signal name
type Webhook struct {
	URL       string
	Condition string
}

// WebhookCache is an in-memory map of:
//
//	tokenId -> map[signalName] -> []Webhook
type WebhookCache struct {
	webhooks map[uint32]map[string][]Webhook
}

// NewWebhookCache returns a cache with some demo data. TODO: what would the real data be?
func NewWebhookCache() *WebhookCache {
	return &WebhookCache{
		webhooks: map[uint32]map[string][]Webhook{
			//this is just an example for now!
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

// GetWebhooks returns the list of webhooks for a given tokenId and signal name
func (wc *WebhookCache) GetWebhooks(tokenId uint32, signalName string) []Webhook {
	byToken, ok := wc.webhooks[tokenId]
	if !ok {
		return nil
	}
	return byToken[signalName]
}
