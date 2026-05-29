package nats

import (
	"context"
	"errors"
	"fmt"

	"github.com/nats-io/nats.go/jetstream"
)

// SignalHistory returns the signal-history KV bucket (per-vehicle per-metric
// last fire snapshot).
func (c *Client) SignalHistory(ctx context.Context) (jetstream.KeyValue, error) {
	return c.kv(ctx, c.cfg.SignalHistoryBucket)
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
