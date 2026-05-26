package nats

import (
	"context"
	"fmt"

	"github.com/nats-io/nats.go/jetstream"
)

// EnsureStreams creates-or-updates the four streams the service uses:
// DIMO_SIGNALS, DIMO_EVENTS, DIMO_TRIGGER_AUDIT, DIMO_TRIGGER_DLQ. Idempotent.
func (c *Client) EnsureStreams(ctx context.Context) error {
	streams := []jetstream.StreamConfig{
		{
			Name:        c.cfg.SignalsStream,
			Subjects:    []string{c.cfg.SignalsSubject},
			Retention:   jetstream.LimitsPolicy,
			Discard:     jetstream.DiscardOld,
			Storage:     jetstream.FileStorage,
			MaxAge:      c.cfg.SignalsMaxAge,
			Replicas:    c.cfg.StreamReplicas,
			Description: "DIMO vehicle signal telemetry",
		},
		{
			Name:        c.cfg.EventsStream,
			Subjects:    []string{c.cfg.EventsSubject},
			Retention:   jetstream.LimitsPolicy,
			Discard:     jetstream.DiscardOld,
			Storage:     jetstream.FileStorage,
			MaxAge:      c.cfg.EventsMaxAge,
			Replicas:    c.cfg.StreamReplicas,
			Description: "DIMO vehicle events",
		},
		{
			Name:        c.cfg.AuditStream,
			Subjects:    []string{c.cfg.AuditSubject},
			Retention:   jetstream.LimitsPolicy,
			Discard:     jetstream.DiscardOld,
			Storage:     jetstream.FileStorage,
			MaxAge:      c.cfg.AuditMaxAge,
			Replicas:    c.cfg.StreamReplicas,
			Description: "Trigger fire audit log for billing",
		},
		{
			Name:        c.cfg.DLQStream,
			Subjects:    []string{c.cfg.DLQSubject},
			Retention:   jetstream.LimitsPolicy,
			Discard:     jetstream.DiscardOld,
			Storage:     jetstream.FileStorage,
			MaxAge:      c.cfg.DLQMaxAge,
			Replicas:    c.cfg.StreamReplicas,
			Description: "Poison messages that exceeded MaxDeliver retries",
		},
	}
	for _, s := range streams {
		if _, err := c.JS.CreateOrUpdateStream(ctx, s); err != nil {
			return fmt.Errorf("ensure stream %s: %w", s.Name, err)
		}
		c.log.Info().Str("stream", s.Name).Msg("nats stream ready")
	}
	return nil
}

// EnsureBuckets creates-or-updates the two KV buckets: webhooks, trigger_state.
// Idempotent.
func (c *Client) EnsureBuckets(ctx context.Context) error {
	buckets := []jetstream.KeyValueConfig{
		{Bucket: c.cfg.WebhooksBucket, History: 1, Replicas: c.cfg.StreamReplicas, Description: "trigger registry"},
		{Bucket: c.cfg.TriggerStateBucket, History: 1, Replicas: c.cfg.StreamReplicas, TTL: c.cfg.TriggerStateTTL, Description: "per-trigger per-vehicle cooldown/last-value state"},
	}
	for _, b := range buckets {
		if _, err := c.JS.CreateOrUpdateKeyValue(ctx, b); err != nil {
			return fmt.Errorf("ensure kv %s: %w", b.Bucket, err)
		}
		c.log.Info().Str("bucket", b.Bucket).Msg("nats kv bucket ready")
	}
	return nil
}
