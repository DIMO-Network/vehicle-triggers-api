package services

import (
	"context"
	"errors"
	"testing"

	"github.com/volatiletech/sqlboiler/v4/boil"
)

func TestPopulateCache(t *testing.T) {
	// Override the dependency injection func
	origFunc := FetchWebhooksFromDBFunc
	defer func() { FetchWebhooksFromDBFunc = origFunc }()

	// fake implementation
	FetchWebhooksFromDBFunc = func(ctx context.Context, exec boil.ContextExecutor) (map[uint32]map[string][]Webhook, error) {
		return map[uint32]map[string][]Webhook{
			10: {
				"temperature": {
					{URL: "http://example.com/webhook", Condition: "valueNumber > 50"},
				},
			},
		}, nil
	}

	cache := NewWebhookCache()
	err := cache.PopulateCache(context.Background(), nil)
	if err != nil {
		t.Fatalf("PopulateCache returned error: %v", err)
	}

	hooks := cache.GetWebhooks(10, "temperature")
	if len(hooks) != 1 {
		t.Errorf("Expected 1 webhook for token 10, 'temperature', got %d", len(hooks))
	}
	if hooks[0].URL != "http://example.com/webhook" {
		t.Errorf("Expected webhook URL 'http://example.com/webhook', got %s", hooks[0].URL)
	}
}

func TestPopulateCache_Error(t *testing.T) {
	origFunc := FetchWebhooksFromDBFunc
	defer func() { FetchWebhooksFromDBFunc = origFunc }()

	FetchWebhooksFromDBFunc = func(ctx context.Context, exec boil.ContextExecutor) (map[uint32]map[string][]Webhook, error) {
		return nil, errors.New("DB error")
	}

	cache := NewWebhookCache()
	err := cache.PopulateCache(context.Background(), nil)
	if err == nil {
		t.Fatalf("Expected error from PopulateCache, got nil")
	}
}
