package nats

import (
	"context"
	"fmt"
	"time"

	"github.com/nats-io/nats.go/jetstream"
)

// ConsumerSpec describes a durable pull consumer bound to a stream.
type ConsumerSpec struct {
	Stream         string
	Durable        string
	FilterSubjects []string
	DeliverPolicy  jetstream.DeliverPolicy
	AckWait        time.Duration
	MaxDeliver     int
	MaxAckPending  int
	BackOff        []time.Duration
	Description    string
}

// DefaultBackOff is the retry backoff ladder for webhook dispatch failures.
var DefaultBackOff = []time.Duration{
	1 * time.Second,
	5 * time.Second,
	30 * time.Second,
	2 * time.Minute,
	10 * time.Minute,
}

// EnsureConsumer creates-or-updates a durable pull consumer for the given spec
// and returns the consumer handle. Callers must set DeliverPolicy explicitly —
// jetstream.DeliverAllPolicy is the zero value and we must not silently
// override it.
func (c *Client) EnsureConsumer(ctx context.Context, spec ConsumerSpec) (jetstream.Consumer, error) {
	if spec.AckWait == 0 {
		spec.AckWait = 45 * time.Second
	}
	if spec.MaxDeliver == 0 {
		spec.MaxDeliver = 5
	}
	if spec.MaxAckPending == 0 {
		spec.MaxAckPending = 5000
	}
	if len(spec.BackOff) == 0 {
		spec.BackOff = DefaultBackOff
	}
	// JetStream requires MaxDeliver > len(BackOff). Trim the ladder so a low
	// MaxDeliver configured via env doesn't crash startup.
	if len(spec.BackOff) >= spec.MaxDeliver {
		spec.BackOff = spec.BackOff[:spec.MaxDeliver-1]
	}

	cfg := jetstream.ConsumerConfig{
		Durable:        spec.Durable,
		Name:           spec.Durable,
		DeliverPolicy:  spec.DeliverPolicy,
		AckPolicy:      jetstream.AckExplicitPolicy,
		AckWait:        spec.AckWait,
		MaxDeliver:     spec.MaxDeliver,
		MaxAckPending:  spec.MaxAckPending,
		BackOff:        spec.BackOff,
		FilterSubjects: spec.FilterSubjects,
		Description:    spec.Description,
	}

	// DeliverPolicy (and its OptStart* companions) is immutable once a durable
	// exists: JetStream fixes it at creation. Passing a different value on a
	// subsequent boot (e.g. an operator flips NATS_DELIVER_POLICY new<->all)
	// makes CreateOrUpdateConsumer reject the update and crash startup. Reuse
	// the existing consumer's policy so re-provisioning is a no-op on that
	// field; the configured policy only takes effect on first creation.
	if existing, err := c.JS.Consumer(ctx, spec.Stream, spec.Durable); err == nil {
		ec := existing.CachedInfo().Config
		if ec.DeliverPolicy != cfg.DeliverPolicy {
			c.log.Warn().
				Str("consumer", spec.Durable).
				Str("configured", cfg.DeliverPolicy.String()).
				Str("existing", ec.DeliverPolicy.String()).
				Msg("nats consumer deliver policy is immutable; keeping existing policy")
		}
		cfg.DeliverPolicy = ec.DeliverPolicy
		cfg.OptStartSeq = ec.OptStartSeq
		cfg.OptStartTime = ec.OptStartTime
	}

	cons, err := c.JS.CreateOrUpdateConsumer(ctx, spec.Stream, cfg)
	if err != nil {
		return nil, fmt.Errorf("ensure consumer %s/%s: %w", spec.Stream, spec.Durable, err)
	}
	c.log.Info().Str("stream", spec.Stream).Str("consumer", spec.Durable).Int("filters", len(spec.FilterSubjects)).Msg("nats consumer ready")
	return cons, nil
}
