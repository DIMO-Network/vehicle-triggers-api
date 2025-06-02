package services

import (
	"context"
	"errors"
	"testing"

	"github.com/DIMO-Network/vehicle-events-api/internal/utils"
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
					{URL: "http://example.com", Trigger: "valueNumber>50", Data: "temperature"},
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
				{URL: "u1"}, {URL: "u2"},
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

func TestPopulateCacheNormalization(t *testing.T) {
	rawSignals := []string{
		"Vehicle.Powertrain.CombustionEngine.IsRunning",
		"Vehicle.Powertrain.TractionBattery.CurrentPower",
		"Vehicle.Powertrain.TractionBattery.Charging.IsCharging",
		"Vehicle.TraveledDistance",
		"Vehicle.Powertrain.TractionBattery.StateOfCharge.Current",
		"Vehicle.Powertrain.FuelSystem.RelativeLevel",
		"Vehicle.Powertrain.FuelSystem.AbsoluteLevel",
		"Vehicle.Chassis.Axle.Row1.Wheel.Left.Tire.Pressure",
		"Vehicle.Chassis.Axle.Row1.Wheel.Right.Tire.Pressure",
		"Vehicle.Chassis.Axle.Row2.Wheel.Left.Tire.Pressure",
		"Vehicle.Chassis.Axle.Row2.Wheel.Right.Tire.Pressure",
	}

	// prepare stub data: one hook per raw signal
	stubData := make(map[uint32]map[string][]Webhook)
	const tokenID = 42
	stubData[tokenID] = make(map[string][]Webhook)
	for _, raw := range rawSignals {
		// PopulateCache should re-key it to normalized
		stubData[tokenID][raw] = []Webhook{{
			ID:             "evt1",
			URL:            "http://u",
			Trigger:        "valueNumber>10",
			CooldownPeriod: 0,
			Data:           raw,
		}}
	}

	orig := FetchWebhooksFromDBFunc
	defer func() { FetchWebhooksFromDBFunc = orig }()
	FetchWebhooksFromDBFunc = func(ctx context.Context, exec boil.ContextExecutor) (map[uint32]map[string][]Webhook, error) {
		return stubData, nil
	}

	wc := NewWebhookCache(&nopLogger)
	if err := wc.PopulateCache(context.Background(), nil); err != nil {
		t.Fatalf("PopulateCache failed: %v", err)
	}

	for _, raw := range rawSignals {
		norm := utils.NormalizeSignalName(raw)
		hooks := wc.GetWebhooks(tokenID, norm)
		if len(hooks) != 1 {
			t.Errorf("expected 1 hook under key %q, got %d", norm, len(hooks))
		}
		// raw key should no longer work
		if got := wc.GetWebhooks(tokenID, raw); len(got) != 0 {
			t.Errorf("expected no hooks under raw key %q, got %d", raw, len(got))
		}
	}
}
