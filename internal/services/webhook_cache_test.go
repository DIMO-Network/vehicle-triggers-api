package services

import (
	"context"
	"errors"
	"testing"

	"github.com/rs/zerolog"
	"github.com/volatiletech/sqlboiler/v4/boil"
)

var nopLogger = zerolog.Nop()

func TestPopulateCache(t *testing.T) {
	orig := FetchWebhooksFromDBFunc
	defer func() { FetchWebhooksFromDBFunc = orig }()

	// Fake implementation
	FetchWebhooksFromDBFunc = func(ctx context.Context, exec boil.ContextExecutor) (map[uint32]map[string][]Webhook, error) {
		return map[uint32]map[string][]Webhook{
			10: {
				"temperature": {
					{URL: "http://example.com", Condition: "valueNumber>50", MetricName: "temperature", DeveloperLicenseAddress: nil},
				},
			},
		}, nil
	}

	cache := NewWebhookCache(&nopLogger)
	if err := cache.PopulateCache(context.Background(), nil); err != nil {
		t.Fatalf("PopulateCache returned error: %v", err)
	}
	hooks := cache.GetWebhooks(10, "temperature")
	if len(hooks) != 1 {
		t.Errorf("expected 1 hook, got %d", len(hooks))
	}
}

func TestPopulateCache_Error(t *testing.T) {
	orig := FetchWebhooksFromDBFunc
	defer func() { FetchWebhooksFromDBFunc = orig }()

	FetchWebhooksFromDBFunc = func(ctx context.Context, exec boil.ContextExecutor) (map[uint32]map[string][]Webhook, error) {
		return nil, errors.New("db error")
	}

	cache := NewWebhookCache(&nopLogger)
	if err := cache.PopulateCache(context.Background(), nil); err == nil {
		t.Fatal("expected error, got nil")
	}
}

func TestGetWebhooks_Empty(t *testing.T) {
	cache := NewWebhookCache(&nopLogger)
	// without PopulateCache, lookup should return nil
	if got := cache.GetWebhooks(123, "foo"); got != nil {
		t.Errorf("expected nil slice, got %v", got)
	}
}

func TestUpdateAndGetWebhooks(t *testing.T) {
	data := map[uint32]map[string][]Webhook{
		55: {
			"gps": {
				{URL: "u1", DeveloperLicenseAddress: nil}, {URL: "u2", DeveloperLicenseAddress: nil},
			},
		},
	}
	cache := NewWebhookCache(&nopLogger)
	cache.Update(data)

	hooks := cache.GetWebhooks(55, "gps")
	if len(hooks) != 2 {
		t.Errorf("expected 2 hooks, got %d", len(hooks))
	}
	if hooks[0].URL != "u1" || hooks[1].URL != "u2" {
		t.Errorf("unexpected URLs: %+v", hooks)
	}
}
