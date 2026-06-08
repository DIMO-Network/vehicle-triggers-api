package triggerstate

import (
	"context"
	"encoding/json"
	"sync"
	"time"

	"github.com/DIMO-Network/cloudevent"
)

// InMemoryStore is a process-local fallback Store for setups without NATS.
// It satisfies cooldown and previousValue lookups within one replica but does
// NOT share state across replicas. Use only in single-process tests or in the
// legacy NATS_MODE=off deployment topology that's being phased out.
type InMemoryStore struct {
	mu       sync.RWMutex
	triggers map[string]Record
	metrics  map[string]MetricRecord
}

// NewInMemory returns an empty in-process store.
func NewInMemory() *InMemoryStore {
	return &InMemoryStore{
		triggers: make(map[string]Record),
		metrics:  make(map[string]MetricRecord),
	}
}

// LastFire returns the last fire record for (trigger, vehicle).
func (s *InMemoryStore) LastFire(_ context.Context, triggerID string, vehicleDID cloudevent.ERC721DID) (Record, bool, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	r, ok := s.triggers[TriggerKey(triggerID, vehicleDID)]
	return r, ok, nil
}

// LastMetric returns the last fire record for (vehicle, metric).
func (s *InMemoryStore) LastMetric(_ context.Context, vehicleDID cloudevent.ERC721DID, metricName string) (MetricRecord, bool, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	r, ok := s.metrics[MetricKey(vehicleDID, metricName)]
	return r, ok, nil
}

// RecordFire writes both records under one lock so a concurrent LastFire +
// LastMetric pair sees a consistent view.
func (s *InMemoryStore) RecordFire(_ context.Context, triggerID, metricName string, vehicleDID cloudevent.ERC721DID, at time.Time, snapshot json.RawMessage) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.triggers[TriggerKey(triggerID, vehicleDID)] = Record{
		LastFiredAt:  at.UTC(),
		TriggerID:    triggerID,
		AssetDID:     vehicleDID.String(),
		LastSnapshot: snapshot,
	}
	if metricName != "" {
		s.metrics[MetricKey(vehicleDID, metricName)] = MetricRecord{
			LastFiredAt:  at.UTC(),
			AssetDID:     vehicleDID.String(),
			MetricName:   metricName,
			LastSnapshot: snapshot,
		}
	}
	return nil
}
