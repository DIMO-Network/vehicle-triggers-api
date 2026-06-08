// Package cachebroadcast carries webhook-config change notifications between
// vehicle-triggers-api replicas over NATS so the in-memory webhook cache can
// invalidate within milliseconds instead of waiting for the 5-minute poll.
//
// Topology: every CRUD endpoint in the webhook controller calls Notifier.
// Notify(webhookID, op) after a successful DB write. The notifier publishes
// a small JSON record to a single plain NATS subject (no JetStream - this
// is a fan-out cache invalidation, not durable audit). Every replica
// subscribes with no queue group, so every replica receives every event and
// calls webhookCache.ScheduleRefresh. Debouncing inside the cache collapses
// bursty CRUD into one rebuild.
//
// A missed notification (transient NATS outage) is bounded by the existing
// periodic poll, which now runs much less frequently (default 5 min). The
// poll is the reconciliation safety net.
package cachebroadcast

import (
	"context"
	"encoding/json"
	"fmt"
	"time"

	nc "github.com/nats-io/nats.go"
	"github.com/rs/zerolog"
)

// Subject is the NATS subject used for cache-change notifications. Single
// subject because we want every replica to receive every event - no need to
// shard by webhook ID.
const Subject = "dimo.cache.webhook.changed"

// Op describes the kind of change so subscribers could filter if needed; the
// current implementation triggers the same full rebuild regardless.
type Op string

const (
	OpCreate Op = "create"
	OpUpdate Op = "update"
	OpDelete Op = "delete"
)

// Notification is the payload published on every change.
type Notification struct {
	WebhookID string    `json:"webhookId"`
	Op        Op        `json:"op"`
	At        time.Time `json:"at"`
}

// Notifier publishes change events. Implementations are safe for concurrent
// use from API handlers. The Op argument is a string to keep the interface
// tiny - callers pass an Op typed constant, the implementation just emits.
type Notifier interface {
	Notify(ctx context.Context, webhookID string, op string) error
}

// NoopNotifier is used when no NATS connection is configured; CRUD handlers
// can call it unconditionally without checking nil.
type NoopNotifier struct{}

func (NoopNotifier) Notify(context.Context, string, string) error { return nil }

// NATSNotifier publishes to NATS. Best-effort: a publish failure is logged
// and discarded - subscribers will eventually reconcile via the periodic
// poll on the receiving replicas.
type NATSNotifier struct {
	conn *nc.Conn
	log  zerolog.Logger
}

// NewNATSNotifier wraps an existing connection.
func NewNATSNotifier(conn *nc.Conn, log zerolog.Logger) *NATSNotifier {
	return &NATSNotifier{conn: conn, log: log}
}

// Notify publishes the change event. Returns the underlying NATS error so
// the caller can surface it in tests; production callers should log + ignore.
func (n *NATSNotifier) Notify(_ context.Context, webhookID string, op string) error {
	body, err := json.Marshal(Notification{
		WebhookID: webhookID,
		Op:        Op(op),
		At:        time.Now().UTC(),
	})
	if err != nil {
		return fmt.Errorf("cachebroadcast marshal: %w", err)
	}
	if err := n.conn.Publish(Subject, body); err != nil {
		n.log.Warn().Err(err).Str("webhookId", webhookID).Msg("cache invalidate publish failed")
		return fmt.Errorf("cachebroadcast publish: %w", err)
	}
	return nil
}

// Refresher is the callback Subscriber invokes on each received notification.
// InvalidateTrigger drops the compiled program for one specific trigger so
// the next refresh recompiles it; ScheduleRefreshSilent kicks the refresh
// without re-broadcasting. Together they implement diff-rebuild on the
// receive side: a CRUD on one trigger touches only that trigger's compiled
// program in every replica's cache, not every program.
type Refresher interface {
	InvalidateTrigger(triggerID string)
	ScheduleRefreshSilent(ctx context.Context)
}

// Subscribe registers a long-lived subscription on Subject. The returned
// *nc.Subscription must be unsubscribed during shutdown. Each received
// notification with a non-empty WebhookID invalidates just that trigger's
// compiled program before scheduling the rebuild. Notifications with an
// empty WebhookID (legacy "refresh all") fall back to a full rebuild.
// Decode errors are logged and the message is dropped - schema mismatches
// shouldn't break the cache.
func Subscribe(conn *nc.Conn, ctx context.Context, r Refresher, log zerolog.Logger) (*nc.Subscription, error) {
	sub, err := conn.Subscribe(Subject, func(msg *nc.Msg) {
		var n Notification
		if err := json.Unmarshal(msg.Data, &n); err != nil {
			log.Warn().Err(err).Msg("cache invalidate: decode failed")
			return
		}
		log.Debug().Str("webhookId", n.WebhookID).Str("op", string(n.Op)).Msg("cache invalidate received")
		if n.WebhookID != "" {
			r.InvalidateTrigger(n.WebhookID)
		}
		r.ScheduleRefreshSilent(ctx)
	})
	if err != nil {
		return nil, fmt.Errorf("cachebroadcast subscribe: %w", err)
	}
	return sub, nil
}
