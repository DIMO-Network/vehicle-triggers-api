package nats

import (
	"context"
	"fmt"

	"github.com/nats-io/nats.go/jetstream"
)

// Publish synchronously publishes a payload to the given subject via JetStream.
// Returns the stream sequence assigned by the server, useful as a stable ID.
func (c *Client) Publish(ctx context.Context, subject string, payload []byte, opts ...jetstream.PublishOpt) (uint64, error) {
	ack, err := c.JS.Publish(ctx, subject, payload, opts...)
	if err != nil {
		return 0, fmt.Errorf("js publish %q: %w", subject, err)
	}
	return ack.Sequence, nil
}

// PublishAsync publishes without waiting for server ack. Returns the future so
// callers that want delivery confirmation can select on it. Used by the audit
// publisher where we trade a small loss risk for throughput.
func (c *Client) PublishAsync(subject string, payload []byte, opts ...jetstream.PublishOpt) (jetstream.PubAckFuture, error) {
	f, err := c.JS.PublishAsync(subject, payload, opts...)
	if err != nil {
		return nil, fmt.Errorf("js publish async %q: %w", subject, err)
	}
	return f, nil
}

// PublishAsyncComplete returns a channel closed when all in-flight async
// publishes have been acked. Useful on shutdown.
func (c *Client) PublishAsyncComplete() <-chan struct{} {
	return c.JS.PublishAsyncComplete()
}
