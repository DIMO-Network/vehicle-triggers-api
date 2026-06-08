// Package triggerstate stores evaluator state in NATS JetStream KV buckets so
// signal/event evaluation has zero Postgres reads on the hot path.
//
// Two buckets are used:
//
//   - trigger_state (per trigger + vehicle): last fire timestamp and the
//     payload snapshot from that fire. Drives the cooldown check and the
//     "previous value for THIS trigger" CEL input on the event path. TTL is
//     configurable (NATS_TRIGGER_STATE_TTL, default 7d) and bounds storage at
//     the longest reasonable cooldown window.
//
//   - signal_history (per vehicle + metric): payload snapshot from the most
//     recent fire of ANY trigger on this metric for this vehicle. Drives the
//     "previous value across triggers" CEL input on the signal path. TTL
//     bounded similarly.
//
// Writes happen synchronously after a successful webhook delivery so any
// replica reading next sees the new state. Reads are best-effort: a KV miss
// or error returns "no prior data" so evaluation uses zero-valued previous
// data, matching the first-time-fire case. That trades a single redundant
// fire on a hard NATS outage for never hanging the eval loop on the DB.
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

// Store reads and writes evaluator state. The interface is small so it can be
// swapped or mocked in tests.
type Store interface {
	LastFire(ctx context.Context, triggerID string, vehicleDID cloudevent.ERC721DID) (Record, bool, error)
	LastMetric(ctx context.Context, vehicleDID cloudevent.ERC721DID, metricName string) (MetricRecord, bool, error)
	RecordFire(ctx context.Context, triggerID, metricName string, vehicleDID cloudevent.ERC721DID, at time.Time, snapshot json.RawMessage) error
}

// Record is the JSON payload stored per (trigger, vehicle) key in the
// trigger_state bucket.
type Record struct {
	LastFiredAt  time.Time       `json:"lastFiredAt"`
	TriggerID    string          `json:"triggerId"`
	AssetDID     string          `json:"assetDid"`
	LastSnapshot json.RawMessage `json:"lastSnapshot,omitempty"`
}

// MetricRecord is the JSON payload stored per (vehicle, metric) key in the
// signal_history bucket.
type MetricRecord struct {
	LastFiredAt  time.Time       `json:"lastFiredAt"`
	AssetDID     string          `json:"assetDid"`
	MetricName   string          `json:"metricName"`
	LastSnapshot json.RawMessage `json:"lastSnapshot,omitempty"`
}

// KVStore is a Store backed by two NATS JetStream KV buckets - one per-trigger
// and one per-metric. Both buckets are opened by the caller; passing nil for
// either disables that half (used by tests that exercise only one path).
type KVStore struct {
	state   jetstream.KeyValue
	history jetstream.KeyValue
}

// New wraps existing KV bucket handles. state holds per-(trigger, vehicle)
// records, history holds per-(vehicle, metric) records. Either may be nil.
func New(state, history jetstream.KeyValue) *KVStore {
	return &KVStore{state: state, history: history}
}

// TriggerKey builds the trigger_state bucket key for (triggerID, vehicleDID).
// NATS KV keys allow alphanumeric plus -_=./ — we replace anything else with
// '_' so triggerIDs (UUIDs) and DIDs (did:erc721:...) round-trip cleanly.
func TriggerKey(triggerID string, vehicleDID cloudevent.ERC721DID) string {
	return sanitize(triggerID) + "." + sanitize(vehicleDID.String())
}

// MetricKey builds the signal_history bucket key for (vehicleDID, metricName).
func MetricKey(vehicleDID cloudevent.ERC721DID, metricName string) string {
	return sanitize(vehicleDID.String()) + "." + sanitize(metricName)
}

// Key is kept as an alias for the trigger key for backwards compatibility
// with the CLI and earlier call sites.
func Key(triggerID string, vehicleDID cloudevent.ERC721DID) string {
	return TriggerKey(triggerID, vehicleDID)
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

// LastFire returns the most recent fire record for this (trigger, vehicle).
// The second return value is false when the key is absent.
func (s *KVStore) LastFire(ctx context.Context, triggerID string, vehicleDID cloudevent.ERC721DID) (Record, bool, error) {
	if s == nil || s.state == nil {
		return Record{}, false, nil
	}
	entry, err := s.state.Get(ctx, TriggerKey(triggerID, vehicleDID))
	if err != nil {
		if errors.Is(err, jetstream.ErrKeyNotFound) {
			return Record{}, false, nil
		}
		return Record{}, false, fmt.Errorf("kv get trigger_state: %w", err)
	}
	var r Record
	if err := json.Unmarshal(entry.Value(), &r); err != nil {
		return Record{}, false, fmt.Errorf("kv decode trigger_state: %w", err)
	}
	return r, true, nil
}

// LastMetric returns the most recent fire record for this (vehicle, metric)
// across any trigger. False when absent.
func (s *KVStore) LastMetric(ctx context.Context, vehicleDID cloudevent.ERC721DID, metricName string) (MetricRecord, bool, error) {
	if s == nil || s.history == nil {
		return MetricRecord{}, false, nil
	}
	entry, err := s.history.Get(ctx, MetricKey(vehicleDID, metricName))
	if err != nil {
		if errors.Is(err, jetstream.ErrKeyNotFound) {
			return MetricRecord{}, false, nil
		}
		return MetricRecord{}, false, fmt.Errorf("kv get signal_history: %w", err)
	}
	var r MetricRecord
	if err := json.Unmarshal(entry.Value(), &r); err != nil {
		return MetricRecord{}, false, fmt.Errorf("kv decode signal_history: %w", err)
	}
	return r, true, nil
}

// RecordFire writes both KV records in one logical call.
//
// trigger_state uses optimistic CAS so concurrent writers from two replicas
// on the same (trigger, vehicle) don't both blindly Put. Race semantics:
//
//  1. Read current revision via Get. Marshal new Record.
//  2. Try Update on the observed revision. On revision-mismatch, refresh and
//     retry once - covers the common "another writer just landed" case.
//  3. On a second conflict, fall back to Put (last-writer-wins) and record
//     the conflict on a counter. The other writer already sent its webhook;
//     we already sent ours. Receivers must dedup via the deterministic
//     webhook ID. See PROD_HARDENING.md for the contract.
//
// signal_history is per (vehicle, metric) across triggers - Put is correct
// because the most-recent value wins by definition.
//
// metricName may be empty when the caller does not have a per-metric concept
// (e.g. unit tests); in that case the signal_history write is skipped.
func (s *KVStore) RecordFire(ctx context.Context, triggerID, metricName string, vehicleDID cloudevent.ERC721DID, at time.Time, snapshot json.RawMessage) error {
	now := at.UTC()
	if s != nil && s.state != nil {
		body, err := json.Marshal(Record{
			LastFiredAt:  now,
			TriggerID:    triggerID,
			AssetDID:     vehicleDID.String(),
			LastSnapshot: snapshot,
		})
		if err != nil {
			return fmt.Errorf("kv encode trigger_state: %w", err)
		}
		key := TriggerKey(triggerID, vehicleDID)
		if err := writeWithCAS(ctx, s.state, key, body, "trigger_state"); err != nil {
			return err
		}
	}
	if metricName == "" || s == nil || s.history == nil {
		return nil
	}
	body, err := json.Marshal(MetricRecord{
		LastFiredAt:  now,
		AssetDID:     vehicleDID.String(),
		MetricName:   metricName,
		LastSnapshot: snapshot,
	})
	if err != nil {
		return fmt.Errorf("kv encode signal_history: %w", err)
	}
	if _, err := s.history.Put(ctx, MetricKey(vehicleDID, metricName), body); err != nil {
		return fmt.Errorf("kv put signal_history: %w", err)
	}
	return nil
}

// writeWithCAS executes the CAS-with-retry-with-fallback policy described
// above on the supplied bucket + key. Errors that aren't conflict-shaped
// propagate immediately. bucketLabel is used only for the conflict metric.
func writeWithCAS(ctx context.Context, kv jetstream.KeyValue, key string, body []byte, bucketLabel string) error {
	const maxRetries = 1

	for attempt := 0; attempt <= maxRetries; attempt++ {
		entry, getErr := kv.Get(ctx, key)
		if getErr != nil && !errors.Is(getErr, jetstream.ErrKeyNotFound) {
			return fmt.Errorf("kv get %s: %w", bucketLabel, getErr)
		}
		var rev uint64
		if getErr == nil {
			rev = entry.Revision()
		}

		var writeErr error
		if rev == 0 {
			_, writeErr = kv.Create(ctx, key, body)
		} else {
			_, writeErr = kv.Update(ctx, key, body, rev)
		}
		if writeErr == nil {
			return nil
		}
		if !isConflict(writeErr) {
			return fmt.Errorf("kv write %s: %w", bucketLabel, writeErr)
		}

		if attempt < maxRetries {
			metricsCASConflict(bucketLabel, "retry")
			continue
		}
		// Persistent conflict: write unconditionally so state isn't lost,
		// and surface the race via the conflict counter so ops can see it.
		metricsCASConflict(bucketLabel, "fallback")
		if _, err := kv.Put(ctx, key, body); err != nil {
			return fmt.Errorf("kv put fallback %s: %w", bucketLabel, err)
		}
		return nil
	}
	return nil
}

// isConflict matches the error shapes the KV API returns when an optimistic
// write loses the CAS race. Both Create (key already exists) and Update
// (wrong revision) surface as JSErrCodeStreamWrongLastSequence (10071) under
// the hood; ErrKeyExists is the sentinel for the Create form.
func isConflict(err error) bool {
	if errors.Is(err, jetstream.ErrKeyExists) {
		return true
	}
	var apiErr *jetstream.APIError
	if errors.As(err, &apiErr) {
		return apiErr.ErrorCode == jetstream.JSErrCodeStreamWrongLastSequence
	}
	return false
}
