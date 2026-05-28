package auditqueue

import (
	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/promauto"
)

// Audit queue observability. dropped_total is the key SLO indicator -
// dropped audit records are billing miscounts. A non-zero rate means the
// queue is undersized relative to fire volume or the audit publisher is
// stalled.
var (
	queueDepth = promauto.NewGauge(prometheus.GaugeOpts{
		Namespace: "vehicle_triggers",
		Subsystem: "audit",
		Name:      "queue_depth",
		Help:      "Current number of audit records waiting in the queue.",
	})

	droppedTotal = promauto.NewCounter(prometheus.CounterOpts{
		Namespace: "vehicle_triggers",
		Subsystem: "audit",
		Name:      "dropped_total",
		Help:      "Number of audit records dropped because the queue was full. Each drop is a billing miscount; alarm on this.",
	})

	publishedTotal = promauto.NewCounter(prometheus.CounterOpts{
		Namespace: "vehicle_triggers",
		Subsystem: "audit",
		Name:      "published_total",
		Help:      "Number of audit records successfully published to the audit stream.",
	})

	errorTotal = promauto.NewCounter(prometheus.CounterOpts{
		Namespace: "vehicle_triggers",
		Subsystem: "audit",
		Name:      "publish_errors_total",
		Help:      "Number of audit publish attempts that returned an error from PublishTriggerFired.",
	})
)
