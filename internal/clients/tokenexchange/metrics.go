package tokenexchange

import (
	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/promauto"
)

// Cache observability. The permission cache hit rate is the single biggest
// lever on hot-path latency at scale: a cold miss is a gRPC RTT to
// token-exchange, a hit is a map lookup. We track outcomes so capacity
// planning can see warm-up spikes after autoscale events.
var cacheTotal = promauto.NewCounterVec(prometheus.CounterOpts{
	Namespace: "vehicle_triggers",
	Subsystem: "tokenexchange",
	Name:      "cache_total",
	Help:      "Token-exchange permission cache outcomes. hit = served from local cache, miss = backed gRPC succeeded and cached, error = gRPC failed.",
}, []string{"outcome"})

func metricsCache(outcome string) {
	cacheTotal.WithLabelValues(outcome).Inc()
}
