package nats

import (
	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/promauto"
)

// Prometheus counters covering publish + consume health. Exposed via the
// service's existing /metrics endpoint (server-garage's monserver registers
// the default registry).
var (
	publishTotal = promauto.NewCounterVec(prometheus.CounterOpts{
		Namespace: "vehicle_triggers",
		Subsystem: "nats",
		Name:      "publish_total",
		Help:      "Number of JetStream publishes, labeled by stream and outcome.",
	}, []string{"stream", "outcome"})

	consumeTotal = promauto.NewCounterVec(prometheus.CounterOpts{
		Namespace: "vehicle_triggers",
		Subsystem: "nats",
		Name:      "consume_total",
		Help:      "Number of JetStream messages processed, labeled by stream and outcome (ack|nak).",
	}, []string{"stream", "outcome"})
)

// MetricsPublish records a publish outcome.
func MetricsPublish(stream, outcome string) {
	publishTotal.WithLabelValues(stream, outcome).Inc()
}

// MetricsConsume records a consume outcome.
func MetricsConsume(stream, outcome string) {
	consumeTotal.WithLabelValues(stream, outcome).Inc()
}
