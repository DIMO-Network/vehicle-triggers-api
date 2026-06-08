package nats

import (
	"time"

	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/promauto"
)

// Prometheus instrumentation for JetStream publish + consume health and
// end-to-end evaluation latency. Exposed via the service's existing /metrics
// endpoint (server-garage's monserver registers the default registry).
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
		Help:      "Number of JetStream messages processed, labeled by stream and outcome (ack|nak|dlq).",
	}, []string{"stream", "outcome"})

	// evalLatency is the wall-clock time from when JetStream timestamped the
	// message (i.e. message arrival in the stream) to when our handler
	// returns. It includes our parse + CEL eval + webhook dispatch. SLO
	// surface lives here.
	evalLatency = promauto.NewHistogramVec(prometheus.HistogramOpts{
		Namespace: "vehicle_triggers",
		Subsystem: "nats",
		Name:      "eval_latency_seconds",
		Help:      "Time from JetStream message timestamp to handler return.",
		Buckets:   []float64{0.001, 0.005, 0.01, 0.025, 0.05, 0.1, 0.25, 0.5, 1, 2.5, 5, 10, 30},
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

// MetricsEvalLatency records the end-to-end handler latency for a message.
// arrived is the JetStream message timestamp (when the broker received the
// message); pass time.Time{} when unknown and the observation is skipped.
func MetricsEvalLatency(stream, outcome string, arrived time.Time) {
	if arrived.IsZero() {
		return
	}
	evalLatency.WithLabelValues(stream, outcome).Observe(time.Since(arrived).Seconds())
}
