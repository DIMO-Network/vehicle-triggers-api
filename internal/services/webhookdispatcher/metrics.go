package webhookdispatcher

import (
	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/promauto"
)

// Dispatcher instrumentation. queueDepth is a gauge so dashboards can show
// in-flight pressure; queueFull tracks the rate at which Enqueue rejects
// (the backpressure signal that the caller surfaces by naking the message).
// deliveryTotal + deliveryLatency cover the actual HTTP dispatch outcome.
var (
	queueDepth = promauto.NewGauge(prometheus.GaugeOpts{
		Namespace: "vehicle_triggers",
		Subsystem: "dispatcher",
		Name:      "queue_depth",
		Help:      "Current number of jobs waiting in the dispatcher queue.",
	})

	queueFull = promauto.NewCounter(prometheus.CounterOpts{
		Namespace: "vehicle_triggers",
		Subsystem: "dispatcher",
		Name:      "queue_full_total",
		Help:      "Number of Enqueue rejections due to a full queue. Each rejection naks the inbound JetStream message.",
	})

	deliveryTotal = promauto.NewCounterVec(prometheus.CounterOpts{
		Namespace: "vehicle_triggers",
		Subsystem: "dispatcher",
		Name:      "delivery_total",
		Help:      "Webhook delivery outcomes from the dispatcher path (ok|error).",
	}, []string{"outcome"})

	deliveryLatency = promauto.NewHistogramVec(prometheus.HistogramOpts{
		Namespace: "vehicle_triggers",
		Subsystem: "dispatcher",
		Name:      "delivery_latency_seconds",
		Help:      "Wall clock from dispatcher job start to receiver response.",
		Buckets:   []float64{0.005, 0.025, 0.1, 0.25, 0.5, 1, 2.5, 5, 10, 30},
	}, []string{"outcome"})
)
