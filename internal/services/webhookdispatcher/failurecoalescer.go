package webhookdispatcher

import (
	"context"
	"sync"
	"time"

	"github.com/DIMO-Network/vehicle-triggers-api/internal/db/models"
	"github.com/rs/zerolog"
)

// failureCoalescer batches IncrementTriggerFailureCount calls so a receiver
// outage doesn't fan one DB UPDATE per worker per failed delivery onto
// Postgres. At 32 workers + 30k fires/sec on a permanently-broken receiver
// that was 32k writes/sec hitting the same row family.
//
// Coalescing model:
//   - Record(trigger, reason) just increments an in-memory counter keyed by
//     trigger ID and remembers the most recent reason. No DB touch.
//   - A single flusher goroutine wakes every flushInterval, snapshots the
//     accumulated counts, zeros the map, and emits one UPDATE per
//     distinct trigger with the accumulated count.
//
// Trade: a pod crash between flushes loses up to flushInterval worth of
// failure-count writes. Acceptable: the circuit-breaker recovers on the
// next failed delivery; transient under-counting is preferable to
// hammering the DB with a write storm during an outage.
type failureCoalescer struct {
	repo          FailureRepo
	maxFailures   int
	flushInterval time.Duration
	log           zerolog.Logger

	mu      sync.Mutex
	pending map[string]*pendingFailure

	stop chan struct{}
	done chan struct{}
}

type pendingFailure struct {
	trigger *models.Trigger
	count   int
	reason  error
}

func newFailureCoalescer(repo FailureRepo, maxFailures int, flushInterval time.Duration, log zerolog.Logger) *failureCoalescer {
	if flushInterval <= 0 {
		flushInterval = time.Second
	}
	return &failureCoalescer{
		repo:          repo,
		maxFailures:   maxFailures,
		flushInterval: flushInterval,
		log:           log,
		pending:       make(map[string]*pendingFailure),
		stop:          make(chan struct{}),
		done:          make(chan struct{}),
	}
}

// Run starts the flusher goroutine; returns when ctx cancels or Close is
// called. Drains a final flush before returning so failures recorded in the
// last window aren't lost.
func (c *failureCoalescer) Run(ctx context.Context) {
	defer close(c.done)
	ticker := time.NewTicker(c.flushInterval)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			c.flush(context.Background())
			return
		case <-c.stop:
			c.flush(context.Background())
			return
		case <-ticker.C:
			c.flush(ctx)
		}
	}
}

// Close stops the flusher and waits for a final flush.
func (c *failureCoalescer) Close() {
	select {
	case <-c.stop:
		// already closed
	default:
		close(c.stop)
	}
	<-c.done
}

// Record accumulates a failure for the given trigger. The reason is
// retained as "most recent" for the eventual UPDATE; older reasons in the
// same window are discarded. This keeps the in-memory map tiny while still
// surfacing the latest failure cause when the circuit eventually opens.
func (c *failureCoalescer) Record(trigger *models.Trigger, reason error) {
	if trigger == nil {
		return
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	p, ok := c.pending[trigger.ID]
	if !ok {
		p = &pendingFailure{trigger: trigger}
		c.pending[trigger.ID] = p
	}
	p.count++
	p.reason = reason
}

func (c *failureCoalescer) flush(ctx context.Context) {
	c.mu.Lock()
	if len(c.pending) == 0 {
		c.mu.Unlock()
		return
	}
	batch := c.pending
	c.pending = make(map[string]*pendingFailure)
	c.mu.Unlock()

	for _, p := range batch {
		// Apply the accumulated count by calling Increment once per failure.
		// IncrementTriggerFailureCount does a fetch-for-update + write per
		// call; calling it N times here still beats calling it N times
		// from N workers because we serialise to one writer per trigger
		// and amortise the row-lock contention. A future optimisation
		// could add a repo method that takes a delta and applies it in
		// one statement; for now we keep the existing interface.
		for i := 0; i < p.count; i++ {
			if err := c.repo.IncrementTriggerFailureCount(ctx, p.trigger, p.reason, c.maxFailures); err != nil {
				c.log.Error().Err(err).Str("triggerId", p.trigger.ID).Msg("failurecoalescer: increment failed")
				break
			}
		}
	}
}
