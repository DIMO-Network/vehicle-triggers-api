package nats

import (
	"context"
	"errors"
	"time"

	"github.com/nats-io/nats.go/jetstream"
	"golang.org/x/sync/semaphore"
)

// PayloadHandler processes the raw JetStream message body. Returning nil acks
// the message; a non-nil error nak's it so JetStream redelivers per the
// consumer's BackOff ladder.
//
// Wrap with ErrBackpressure to signal that the failure is transient and
// purely a load-shed - the message itself isn't poison. The PullLoop will
// use BackpressureNakDelay instead of the consumer's BackOff, which buys
// time for the downstream queue to drain without burning MaxDeliver budget
// on a self-inflicted condition.
type PayloadHandler func(ctx context.Context, payload []byte) error

// ErrBackpressure marks a handler error as load-shed, not poison. Wrap with
// fmt.Errorf("%w: ...", ErrBackpressure) or errors.Join(ErrBackpressure, ...)
// so the PullLoop classifier picks it up.
var ErrBackpressure = errors.New("nats: handler backpressure")

// BackpressureNakDelay is how long PullLoop holds a backpressure-flagged
// message before redelivery. Default 30s - long enough that we don't churn
// the queue, short enough that a brief spike clears within MaxDeliver
// attempts (e.g. 5 attempts * 30s = 2.5 min total runway).
var BackpressureNakDelay = 30 * time.Second

// PullLoop pulls in batches from the consumer and dispatches each message to
// handler in a bounded worker pool. Returns when ctx is cancelled or the
// consumer's Messages() iterator is closed.
//
// JetStream pull-consumer semantics:
//   - Ack on success.
//   - NakWithDelay on error using the next BackOff step from MaxDeliver state
//     (the server picks the step; we just ask for a redelivery).
//   - Up to maxInFlight handler goroutines run concurrently per pull loop.
func (c *Client) PullLoop(ctx context.Context, cons jetstream.Consumer, maxInFlight int, handler PayloadHandler) error {
	if maxInFlight < 1 {
		maxInFlight = 1
	}
	log := c.log.With().Str("component", "nats.pullloop").Logger()

	sem := semaphore.NewWeighted(int64(maxInFlight))
	waitForInFlight := func() {
		_ = sem.Acquire(context.Background(), int64(maxInFlight))
		sem.Release(int64(maxInFlight))
	}

	iter, err := cons.Messages(jetstream.PullMaxMessages(c.cfg.FetchBatch))
	if err != nil {
		return err
	}
	defer iter.Stop()

	// Cancel iterator when ctx cancels.
	go func() {
		<-ctx.Done()
		iter.Stop()
	}()

	for {
		msg, err := iter.Next()
		if err != nil {
			if errors.Is(err, jetstream.ErrMsgIteratorClosed) || ctx.Err() != nil {
				waitForInFlight()
				return ctx.Err()
			}
			log.Error().Err(err).Msg("pull iterator error")
			continue
		}

		if err := sem.Acquire(ctx, 1); err != nil {
			// Context cancelled. Nak so the message redelivers on the next leader.
			_ = msg.Nak()
			waitForInFlight()
			return ctx.Err()
		}

		go func(m jetstream.Msg) {
			defer sem.Release(1)
			meta, _ := m.Metadata()
			stream := ""
			var numDelivered uint64
			var arrived time.Time
			if meta != nil {
				stream = meta.Stream
				numDelivered = meta.NumDelivered
				arrived = meta.Timestamp
			}
			payload := m.Data()
			if err := handler(ctx, payload); err != nil {
				// Backpressure: handler refused work because a downstream
				// queue was full. Nak with a long delay so we don't burn
				// through MaxDeliver while the queue drains. We still count
				// these against MaxDeliver (JetStream doesn't let us not),
				// but the long delay gives a default 5x30s = 2.5min window
				// before DLQ, which is plenty for a transient spike.
				if errors.Is(err, ErrBackpressure) {
					log.Warn().Err(err).Str("subject", m.Subject()).Uint64("attempt", numDelivered).Msg("backpressure; nak with delay")
					_ = m.NakWithDelay(BackpressureNakDelay)
					MetricsConsume(stream, "nak_backpressure")
					MetricsEvalLatency(stream, "nak_backpressure", arrived)
					return
				}
				// Last attempt? Park it in the DLQ and terminally fail so the
				// stream doesn't keep redelivering forever or silently drop
				// after MaxDeliver expires.
				if c.cfg.MaxDeliver > 0 && numDelivered >= uint64(c.cfg.MaxDeliver) {
					if dlqErr := c.publishDLQ(m, err); dlqErr != nil {
						log.Error().Err(dlqErr).Str("subject", m.Subject()).Msg("dlq publish failed; falling back to nak")
						_ = m.NakWithDelay(0)
						MetricsConsume(stream, "nak")
						MetricsEvalLatency(stream, "nak", arrived)
						return
					}
					_ = m.Term()
					MetricsConsume(stream, "dlq")
					MetricsEvalLatency(stream, "dlq", arrived)
					return
				}
				log.Error().Err(err).Str("subject", m.Subject()).Uint64("attempt", numDelivered).Msg("handler failed; nak")
				_ = m.NakWithDelay(0)
				MetricsConsume(stream, "nak")
				MetricsEvalLatency(stream, "nak", arrived)
				return
			}
			if err := m.Ack(); err != nil {
				log.Warn().Err(err).Str("subject", m.Subject()).Msg("ack failed")
			}
			MetricsConsume(stream, "ack")
			MetricsEvalLatency(stream, "ack", arrived)
		}(msg)
	}
}

// MustWaitFor blocks up to d for the JetStream account info round-trip. Used
// for startup sanity checks where the caller wants a single short timeout
// instead of layering its own context.
func (c *Client) MustWaitFor(ctx context.Context, d time.Duration) error {
	if c == nil || c.JS == nil {
		return errors.New("nats client not initialized")
	}
	cctx, cancel := context.WithTimeout(ctx, d)
	defer cancel()
	_, err := c.JS.AccountInfo(cctx)
	return err
}
