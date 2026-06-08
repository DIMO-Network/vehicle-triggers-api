// Package configaudit publishes an immutable record of every webhook-config
// CRUD to JetStream. The audit stream (DIMO_CONFIG_AUDIT) lives separately
// from the trigger-fired audit so config changes don't share retention or
// throughput characteristics with hot-path fires. Receivers (compliance,
// ops dashboards, change-management tooling) consume the stream however
// they like; the service only emits.
//
// Publishes are best-effort: a failure is logged and discarded. The DB row
// is the source of truth; the audit stream is a historical record. If NATS
// is unavailable, the API request still succeeds.
package configaudit

import (
	"context"
	"encoding/json"
	"fmt"
	"time"

	"github.com/DIMO-Network/vehicle-triggers-api/internal/nats"
)

// Subject is the JetStream subject root. Keyed on webhookID so consumers
// can filter to a single webhook's history without server-side replay.
const SubjectPrefix = "dimo.config.changed"

// Op is the lifecycle event type.
type Op string

const (
	OpWebhookCreate     Op = "webhook.create"
	OpWebhookUpdate     Op = "webhook.update"
	OpWebhookDelete     Op = "webhook.delete"
	OpSubscribeVehicle  Op = "subscription.create"
	OpUnsubscribeVehicle Op = "subscription.delete"
)

// Event is the JSON payload.
type Event struct {
	Op           Op             `json:"op"`
	At           time.Time      `json:"at"`
	WebhookID    string         `json:"webhookId,omitempty"`
	DevLicense   string         `json:"devLicense,omitempty"`
	AssetDID     string         `json:"assetDid,omitempty"`
	Snapshot     map[string]any `json:"snapshot,omitempty"`
}

// Publisher is the interface API handlers depend on. Noop is used when NATS
// is not configured.
type Publisher interface {
	Publish(ctx context.Context, e Event) error
}

// Noop discards events; safe to use as a default so handlers don't need
// nil-checks.
type Noop struct{}

func (Noop) Publish(context.Context, Event) error { return nil }

// NATSPublisher writes events to the audit JetStream stream.
type NATSPublisher struct {
	client *nats.Client
}

// New wraps a NATS client; nil client returns a Noop.
func New(client *nats.Client) Publisher {
	if client == nil {
		return Noop{}
	}
	return &NATSPublisher{client: client}
}

// Publish emits the event. The subject embeds the webhook ID when present
// so consumers can filter to a specific webhook history; events without a
// webhook ID (e.g. global config changes, not currently emitted) land on a
// catch-all subject.
func (p *NATSPublisher) Publish(ctx context.Context, e Event) error {
	if e.At.IsZero() {
		e.At = time.Now().UTC()
	}
	body, err := json.Marshal(e)
	if err != nil {
		return fmt.Errorf("configaudit marshal: %w", err)
	}
	subject := SubjectPrefix + "._unknown"
	if e.WebhookID != "" {
		subject = SubjectPrefix + "." + sanitize(e.WebhookID)
	}
	if _, err := p.client.Publish(ctx, subject, body); err != nil {
		return fmt.Errorf("configaudit publish %q: %w", subject, err)
	}
	return nil
}

// sanitize matches the NATS subject sanitization elsewhere in the service.
// We don't import internal/nats's helper because it's package-private; the
// rules are tiny enough to duplicate.
func sanitize(s string) string {
	out := make([]byte, 0, len(s))
	for _, c := range []byte(s) {
		switch c {
		case ' ', '.', '*', '>', '\t', '\n', '\r':
			out = append(out, '_')
		default:
			out = append(out, c)
		}
	}
	if len(out) == 0 {
		return "_"
	}
	return string(out)
}
