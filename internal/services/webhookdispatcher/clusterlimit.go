package webhookdispatcher

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"hash/fnv"
	"sync"
	"time"

	"github.com/nats-io/nats.go/jetstream"
)

// clusterLimiter is the cluster-shared cousin of hostLimiter. It stores a
// per-host token-bucket state in a JetStream KV bucket, so N pods sharing
// the same configured RPS truly add up to RPS aggregate, not N*RPS.
//
// Each bucket entry is a small JSON record with the current token count
// and last-refill timestamp. Acquisition is a CAS loop: Get -> compute new
// tokens -> Update with the previous revision. CAS conflicts indicate
// contention; the caller retries after a small backoff. This is more
// expensive than a per-pod limiter (one KV round-trip per send vs. one
// in-memory CAS), so it only kicks in when explicitly enabled.
//
// Wire it up by passing a non-nil jetstream.KeyValue to newClusterLimiter.
// Falls back to the per-pod hostLimiter when the KV bucket is unavailable,
// so a JetStream outage degrades to "weaker but still bounded" rather
// than "unlimited."
type clusterLimiter struct {
	kv          jetstream.KeyValue
	rps         float64
	burst       int
	fallback    *hostLimiter // used when KV operations fail
	maxRetries  int
	pollDelay   time.Duration
	now         func() time.Time

	// In-process backoff sleep cache so contention on a hot key doesn't
	// pile every worker against KV at once. Each host keeps its own
	// "next allowed" timestamp updated when the bucket was empty.
	mu       sync.Mutex
	nextWake map[string]time.Time
}

type bucketState struct {
	Tokens     float64   `json:"t"`
	LastRefill time.Time `json:"r"`
}

func newClusterLimiter(kv jetstream.KeyValue, rps float64, burst int, fallback *hostLimiter) *clusterLimiter {
	if rps <= 0 || kv == nil {
		return nil
	}
	if burst < 1 {
		burst = 1
	}
	return &clusterLimiter{
		kv:         kv,
		rps:        rps,
		burst:      burst,
		fallback:   fallback,
		maxRetries: 8,
		pollDelay:  50 * time.Millisecond,
		now:        time.Now,
		nextWake:   make(map[string]time.Time),
	}
}

// Wait blocks until a token is available for `target`'s host, or ctx is
// done. Falls back to the per-pod limiter on persistent KV errors so a
// JetStream blip doesn't unbound outbound throughput.
func (c *clusterLimiter) Wait(ctx context.Context, target string) error {
	if c == nil {
		return nil
	}
	host := extractHost(target)
	key := kvKey(host)

	// Fast path: respect any cached "wait until" so a hot empty bucket
	// doesn't have every worker hammering KV in lockstep.
	c.mu.Lock()
	wake, ok := c.nextWake[host]
	c.mu.Unlock()
	if ok && wake.After(c.now()) {
		select {
		case <-time.After(time.Until(wake)):
		case <-ctx.Done():
			return ctx.Err()
		}
	}

	for attempt := 0; attempt < c.maxRetries; attempt++ {
		if err := ctx.Err(); err != nil {
			return err
		}
		entry, rev, err := c.getOrInit(ctx, key)
		if err != nil {
			// KV unreachable - lean on the per-pod fallback so we stay
			// bounded rather than unbounded.
			if c.fallback != nil {
				return c.fallback.Wait(ctx, target)
			}
			return err
		}
		now := c.now()
		c.refill(&entry, now)
		if entry.Tokens >= 1 {
			entry.Tokens--
			if err := c.update(ctx, key, entry, rev); err != nil {
				// CAS conflict - retry the loop. Brief backoff so the
				// retry burst doesn't immediately re-collide.
				select {
				case <-time.After(time.Duration(attempt+1) * c.pollDelay / 4):
				case <-ctx.Done():
					return ctx.Err()
				}
				continue
			}
			return nil
		}
		// Empty bucket: sleep until at least one token is expected, then
		// retry. We don't try to be clever about token-debt scheduling -
		// the limit is approximate by design, the goal is bounded send
		// rate across replicas, not perfect fairness.
		sleep := time.Duration(float64(time.Second) / c.rps)
		c.mu.Lock()
		c.nextWake[host] = now.Add(sleep)
		c.mu.Unlock()
		select {
		case <-time.After(sleep):
		case <-ctx.Done():
			return ctx.Err()
		}
	}
	// Persistent CAS contention: drop to fallback so we don't block forever.
	if c.fallback != nil {
		return c.fallback.Wait(ctx, target)
	}
	return fmt.Errorf("clusterlimit: exhausted retries on %s", host)
}

func (c *clusterLimiter) refill(b *bucketState, now time.Time) {
	if b.LastRefill.IsZero() {
		b.LastRefill = now
		b.Tokens = float64(c.burst)
		return
	}
	elapsed := now.Sub(b.LastRefill).Seconds()
	if elapsed <= 0 {
		return
	}
	b.Tokens += elapsed * c.rps
	if b.Tokens > float64(c.burst) {
		b.Tokens = float64(c.burst)
	}
	b.LastRefill = now
}

func (c *clusterLimiter) getOrInit(ctx context.Context, key string) (bucketState, uint64, error) {
	entry, err := c.kv.Get(ctx, key)
	if err != nil {
		if errors.Is(err, jetstream.ErrKeyNotFound) {
			// Treat missing as a full bucket with revision 0 so the
			// first Update either creates the key (success) or sees a
			// race-created revision (retry).
			return bucketState{Tokens: float64(c.burst), LastRefill: c.now()}, 0, nil
		}
		return bucketState{}, 0, fmt.Errorf("kv get %s: %w", key, err)
	}
	var st bucketState
	if uerr := json.Unmarshal(entry.Value(), &st); uerr != nil {
		// Corrupt entry: reset.
		return bucketState{Tokens: float64(c.burst), LastRefill: c.now()}, entry.Revision(), nil
	}
	return st, entry.Revision(), nil
}

func (c *clusterLimiter) update(ctx context.Context, key string, st bucketState, rev uint64) error {
	body, err := json.Marshal(st)
	if err != nil {
		return err
	}
	if rev == 0 {
		_, err = c.kv.Create(ctx, key, body)
	} else {
		_, err = c.kv.Update(ctx, key, body, rev)
	}
	if err != nil {
		return fmt.Errorf("kv update %s rev=%d: %w", key, rev, err)
	}
	return nil
}

// kvKey returns a NATS-subject-safe key derived from the host string.
// We hash to keep cardinality bounded (a misconfigured trigger with a
// novel host per signal would otherwise grow the bucket without bound).
// FNV-1a is fine: collisions just share a budget which is the right
// behaviour for "limit per receiver."
func kvKey(host string) string {
	h := fnv.New64a()
	_, _ = h.Write([]byte(host))
	return fmt.Sprintf("host_%016x", h.Sum64())
}
