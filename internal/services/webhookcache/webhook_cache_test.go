package webhookcache

import (
	"context"
	"errors"
	"testing"

	"github.com/DIMO-Network/vehicle-triggers-api/internal/services/triggersrepo"
	"github.com/ethereum/go-ethereum/common"
	"github.com/rs/zerolog"
)

var nopLogger = zerolog.Nop()

func TestPopulateCache(t *testing.T) {
	orig := FetchWebhooksFromDBFunc
	defer func() { FetchWebhooksFromDBFunc = orig }()

	// Fake implementation
	FetchWebhooksFromDBFunc = func(ctx context.Context, repo *triggersrepo.Repository) (map[uint32]map[string][]Webhook, error) {
		return map[uint32]map[string][]Webhook{
			10: {
				"temperature": {
					{URL: "http://example.com", Condition: "valueNumber>50", MetricName: "temperature", DeveloperLicenseAddress: common.Address{}},
				},
			},
		}, nil
	}

	cache := NewWebhookCache(nil, &nopLogger)
	if err := cache.PopulateCache(context.Background()); err != nil {
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

	FetchWebhooksFromDBFunc = func(ctx context.Context, repo *triggersrepo.Repository) (map[uint32]map[string][]Webhook, error) {
		return nil, errors.New("db error")
	}

	cache := NewWebhookCache(nil, &nopLogger)
	if err := cache.PopulateCache(context.Background()); err == nil {
		t.Fatal("expected error, got nil")
	}
}

func TestGetWebhooks_Empty(t *testing.T) {
	cache := NewWebhookCache(nil, &nopLogger)
	// without PopulateCache, lookup should return nil
	if got := cache.GetWebhooks(123, "foo"); got != nil {
		t.Errorf("expected nil slice, got %v", got)
	}
}

func TestUpdateAndGetWebhooks(t *testing.T) {
	data := map[uint32]map[string][]Webhook{
		55: {
			"gps": {
				{URL: "u1", DeveloperLicenseAddress: common.Address{}}, {URL: "u2", DeveloperLicenseAddress: common.Address{}},
			},
		},
	}
	cache := NewWebhookCache(nil, &nopLogger)
	cache.Update(data)

	hooks := cache.GetWebhooks(55, "gps")
	if len(hooks) != 2 {
		t.Errorf("expected 2 hooks, got %d", len(hooks))
	}
	if hooks[0].URL != "u1" || hooks[1].URL != "u2" {
		t.Errorf("unexpected URLs: %+v", hooks)
	}
}
