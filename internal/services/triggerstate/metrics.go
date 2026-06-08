package triggerstate

import (
	"sync/atomic"

	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/promauto"
)

// Prometheus instrumentation for the evaluator state path. Counters surface
// the rate of concurrent-writer races so we can see how often the
// at-least-once delivery contract is actually producing duplicate fires in
// production. Decode-error counters surface silent data corruption that
// would otherwise just degrade the previousValue lookup to a zero default.
// sampledErrors is a parallel atomic counter to the prom decodeErrors metric.
// Prom counters don't expose their value publicly; we keep our own so the
// "sample every Nth" decision in MetricsDecodeErrorWithSample is cheap and
// deterministic without scraping our own metrics.
var sampledErrors atomic.Uint64

var (
	casConflicts = promauto.NewCounterVec(prometheus.CounterOpts{
		Namespace: "vehicle_triggers",
		Subsystem: "state",
		Name:      "cas_conflicts_total",
		Help:      "Number of CAS conflicts observed on the per-trigger state bucket. A non-zero rate means two replicas raced on the same (trigger, vehicle); see PROD_HARDENING.md for the receiver-dedup contract.",
	}, []string{"bucket", "outcome"})

	decodeErrors = promauto.NewCounterVec(prometheus.CounterOpts{
		Namespace: "vehicle_triggers",
		Subsystem: "state",
		Name:      "decode_errors_total",
		Help:      "Number of KV records the service failed to JSON-decode. A non-zero rate means writers and readers disagree on the schema or the bucket has corruption.",
	}, []string{"bucket"})
)

// metricsCASConflict records the outcome of an attempted CAS write.
// outcome ∈ {retry, fallback}. retry = first conflict that we resolved by
// re-fetching and retrying. fallback = persistent conflict; we wrote
// unconditionally and the receiver must dedup.
func metricsCASConflict(bucket, outcome string) {
	casConflicts.WithLabelValues(bucket, outcome).Inc()
}

// MetricsDecodeError records a single decode failure on the named bucket.
// Exported so the evaluator can call it from its read path.
func MetricsDecodeError(bucket string) {
	decodeErrors.WithLabelValues(bucket).Inc()
}

// MetricsDecodeErrorWithSample bumps the counter and, every sampleEvery
// errors, returns a non-empty preview of the bad payload (truncated). The
// caller is responsible for logging that preview - we return rather than
// log here so we don't take a dependency on a logger. Returns "" when we
// shouldn't sample this occurrence.
//
// The intent is to surface enough of a real malformed payload that ops can
// reproduce locally without flooding logs at high error rates.
func MetricsDecodeErrorWithSample(bucket string, payload []byte, sampleEvery int) string {
	MetricsDecodeError(bucket)
	if sampleEvery < 1 {
		sampleEvery = 100
	}
	n := sampledErrors.Add(1)
	if int(n%uint64(sampleEvery)) != 0 {
		return ""
	}
	const max = 200
	if len(payload) > max {
		return string(payload[:max]) + "...(truncated)"
	}
	return string(payload)
}
