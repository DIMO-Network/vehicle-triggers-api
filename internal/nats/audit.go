package nats

import (
	"context"
	"fmt"
)

// PublishTriggerFired writes a per-fire audit record to the audit stream
// keyed on developer license. Uses async publish so a stalled audit stream
// can't backpressure the evaluation path; the in-flight ceiling is governed
// by NATS_PUBLISH_ASYNC_MAX_PENDING.
//
// Lives in audit.go (was bridge.go) since the Kafka->NATS republish helpers
// were ripped out post-cutover. The audit stream is the only publish path
// the service owns now; everything else is a consume + dispatch.
func (c *Client) PublishTriggerFired(ctx context.Context, devLicense string, record []byte) error {
	subject := AuditSubject(devLicense)
	if _, err := c.JS.PublishAsync(subject, record); err != nil {
		MetricsPublish(c.cfg.AuditStream, "error")
		return fmt.Errorf("audit publish %q: %w", subject, err)
	}
	MetricsPublish(c.cfg.AuditStream, "ok")
	return nil
}
