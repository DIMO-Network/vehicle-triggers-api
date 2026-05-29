package webhookdispatcher

import (
	"context"
	"net/url"
	"sync"

	"golang.org/x/time/rate"
)

// hostLimiter is a tiny per-host token bucket so one popular webhook
// receiver doesn't get hammered into rate-limiting our entire pod fleet.
// The limit is per-pod, not cluster-global: combined send rate at N pods is
// N * Rate. That's intentionally lenient - true cluster coordination would
// add a KV roundtrip per send. If we ever need cluster-global limits the
// hook is well-isolated.
//
// Limits per HOST (scheme+host of the trigger.TargetURI), not per trigger
// or per developer, so multiple webhooks on the same receiver share the
// budget naturally.
type hostLimiter struct {
	mu        sync.Mutex
	limiters  map[string]*rate.Limiter
	rps       rate.Limit
	burst     int
}

// newHostLimiter builds a limiter that allows `rps` requests per second per
// host, with `burst` immediate tokens. rps <= 0 disables limiting entirely
// (returns a permissive limiter so callers don't need nil checks).
func newHostLimiter(rps float64, burst int) *hostLimiter {
	if rps <= 0 {
		return nil
	}
	if burst < 1 {
		burst = 1
	}
	return &hostLimiter{
		limiters: make(map[string]*rate.Limiter),
		rps:      rate.Limit(rps),
		burst:    burst,
	}
}

// Wait blocks until the per-host token bucket has a slot, or ctx is done.
// nil receiver = unlimited (production passthrough when config disabled it).
func (h *hostLimiter) Wait(ctx context.Context, target string) error {
	if h == nil {
		return nil
	}
	host := extractHost(target)
	h.mu.Lock()
	l, ok := h.limiters[host]
	if !ok {
		l = rate.NewLimiter(h.rps, h.burst)
		h.limiters[host] = l
	}
	h.mu.Unlock()
	return l.Wait(ctx)
}

// extractHost returns scheme+host (no port stripping; receivers on different
// ports are usually different services).
func extractHost(target string) string {
	u, err := url.Parse(target)
	if err != nil || u.Host == "" {
		return target
	}
	return u.Scheme + "://" + u.Host
}
