// Package triggerstate stores per-trigger per-vehicle fire history in a NATS
// JetStream KV bucket. It exists so the evaluator can check cooldown without
// hitting Postgres on every signal, and so multiple service replicas share a
// single source of truth for "did this trigger fire recently?"
//
// The bucket has a TTL (NATS_TRIGGER_STATE_TTL, default 7d) that bounds
// storage and matches the longest reasonable cooldown window. Reads and
// writes are best-effort: any KV error bubbles back as a miss so callers can
// fall through to the database-backed log, keeping correctness even if NATS
// is unreachable.
package triggerstate

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/DIMO-Network/cloudevent"
	"github.com/nats-io/nats.go/jetstream"
)

// Store reads and writes per-(trigger, vehicle) fire state. The interface is
// small so it can be swapped or mocked in tests.
type Store interface {
	LastFire(ctx context.Context, triggerID string, vehicleDID cloudevent.ERC721DID) (time.Time, bool, error)
	RecordFire(ctx context.Context, triggerID string, vehicleDID cloudevent.ERC721DID, at time.Time) error
}

// Record is the JSON payload stored per key.
type Record struct {
	LastFiredAt time.Time `json:"lastFiredAt"`
	TriggerID   string    `json:"triggerId"`
	AssetDID    string    `json:"assetDid"`
}

// KVStore is a Store backed by a NATS JetStream KV bucket.
type KVStore struct {
	kv jetstream.KeyValue
}

// New wraps an existing KV bucket handle.
func New(kv jetstream.KeyValue) *KVStore {
	return &KVStore{kv: kv}
}

// Key builds the bucket key for (triggerID, vehicleDID). NATS KV keys allow
// alphanumeric plus -_=./ — we replace anything else with '_' so triggerIDs
// (UUIDs) and DIDs (did:erc721:...) round-trip cleanly.
func Key(triggerID string, vehicleDID cloudevent.ERC721DID) string {
	return sanitize(triggerID) + "." + sanitize(vehicleDID.String())
}

func sanitize(s string) string {
	r := strings.NewReplacer(
		":", "_",
		" ", "_",
		"*", "_",
		">", "_",
	)
	return r.Replace(s)
}

// LastFire returns the timestamp of the most recent fire for this trigger +
// vehicle. The second return value is false when the key is absent (no prior
// fire on record). Any other KV error is returned as-is so callers can decide
// whether to fall back to the database.
func (s *KVStore) LastFire(ctx context.Context, triggerID string, vehicleDID cloudevent.ERC721DID) (time.Time, bool, error) {
	entry, err := s.kv.Get(ctx, Key(triggerID, vehicleDID))
	if err != nil {
		if errors.Is(err, jetstream.ErrKeyNotFound) {
			return time.Time{}, false, nil
		}
		return time.Time{}, false, fmt.Errorf("kv get: %w", err)
	}
	var r Record
	if err := json.Unmarshal(entry.Value(), &r); err != nil {
		return time.Time{}, false, fmt.Errorf("kv decode: %w", err)
	}
	return r.LastFiredAt, true, nil
}

// RecordFire writes the fire timestamp. Uses Put (last-writer-wins) rather
// than Update because two replicas racing on the same (trigger, vehicle)
// after a tied cooldown window should both succeed; the later timestamp is
// what we want preserved.
func (s *KVStore) RecordFire(ctx context.Context, triggerID string, vehicleDID cloudevent.ERC721DID, at time.Time) error {
	body, err := json.Marshal(Record{
		LastFiredAt: at.UTC(),
		TriggerID:   triggerID,
		AssetDID:    vehicleDID.String(),
	})
	if err != nil {
		return fmt.Errorf("kv encode: %w", err)
	}
	if _, err := s.kv.Put(ctx, Key(triggerID, vehicleDID), body); err != nil {
		return fmt.Errorf("kv put: %w", err)
	}
	return nil
}
