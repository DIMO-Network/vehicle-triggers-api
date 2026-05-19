package nats

import (
	"context"
	"errors"
	"fmt"

	"github.com/nats-io/nats.go/jetstream"
)

// Webhooks returns the webhook-registry KV bucket.
func (c *Client) Webhooks(ctx context.Context) (jetstream.KeyValue, error) {
	return c.kv(ctx, c.cfg.WebhooksBucket)
}

// SignalIndex returns the signal-index KV bucket (active signal/event refcounts).
func (c *Client) SignalIndex(ctx context.Context) (jetstream.KeyValue, error) {
	return c.kv(ctx, c.cfg.SignalIndexBucket)
}

// TriggerState returns the trigger-state KV bucket.
func (c *Client) TriggerState(ctx context.Context) (jetstream.KeyValue, error) {
	return c.kv(ctx, c.cfg.TriggerStateBucket)
}

func (c *Client) kv(ctx context.Context, bucket string) (jetstream.KeyValue, error) {
	kv, err := c.JS.KeyValue(ctx, bucket)
	if err != nil {
		if errors.Is(err, jetstream.ErrBucketNotFound) {
			return nil, fmt.Errorf("kv bucket %q not found (did provisioning run?): %w", bucket, err)
		}
		return nil, fmt.Errorf("kv bucket %q: %w", bucket, err)
	}
	return kv, nil
}
