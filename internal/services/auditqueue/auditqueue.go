// Package auditqueue is a bounded-buffer fire-and-forget queue in front of
// the trigger-fired audit stream. The dispatcher spawns one goroutine per
// fire to call PublishAsync; at 30k fires/sec with a 5s detached context
// that's tens of thousands of goroutines wedged on a slow audit broker.
// This package collapses those goroutines into a single fixed-size queue
// with a tiny drainer pool, trading "audit always sent" for "service never
// stalls when audit is slow." Audit loss is observable via dropped_total.
package auditqueue

import (
	"context"
	"sync"
	"time"

	"github.com/rs/zerolog"
)

// Publisher is the minimal contract the queue needs from the NATS client.
type Publisher interface {
	PublishTriggerFired(ctx context.Context, devLicense string, record []byte) error
}

// Entry is one queued audit publish. The dispatcher hands us already-
// marshaled bodies because re-marshaling under a queue lock would amplify
// the back-pressure risk.
type Entry struct {
	DevLicense string
	Record     []byte
}

// Config tunes the queue.
type Config struct {
	// Workers is the number of drainer goroutines reading from the buffer.
	// 2-4 is typically plenty since each publish is a sub-ms async ack.
	Workers int
	// Buffer is the channel size. Set comfortably above expected steady-
	// state fire rate * publish RTT.
	Buffer int
	// PublishTimeout caps each individual PublishTriggerFired call.
	PublishTimeout time.Duration
}

// Queue is a bounded buffer of audit publishes drained by a small pool.
type Queue struct {
	cfg       Config
	publisher Publisher
	log       zerolog.Logger
	ch        chan Entry
	wg        sync.WaitGroup
	stop      chan struct{}
	once      sync.Once
}

// New builds a queue. Caller must invoke Run to start the drainer pool.
func New(cfg Config, p Publisher, log zerolog.Logger) *Queue {
	if cfg.Workers < 1 {
		cfg.Workers = 2
	}
	if cfg.Buffer < 1 {
		cfg.Buffer = 1024
	}
	if cfg.PublishTimeout <= 0 {
		cfg.PublishTimeout = 5 * time.Second
	}
	return &Queue{
		cfg:       cfg,
		publisher: p,
		log:       log,
		ch:        make(chan Entry, cfg.Buffer),
		stop:      make(chan struct{}),
	}
}

// Submit queues an entry. Non-blocking: if the buffer is full the entry is
// dropped and dropped_total ticks. Returns true on accept, false on drop.
func (q *Queue) Submit(e Entry) bool {
	select {
	case q.ch <- e:
		queueDepth.Set(float64(len(q.ch)))
		return true
	default:
		droppedTotal.Inc()
		return false
	}
}

// PublishTriggerFired adapts the queue to the webhookdispatcher.AuditPublisher
// interface. Returns nil even on drop: a full audit queue is a known,
// metric-tracked failure mode, not an error the dispatcher should propagate
// back to the JetStream handler.
func (q *Queue) PublishTriggerFired(_ context.Context, devLicense string, record []byte) error {
	q.Submit(Entry{DevLicense: devLicense, Record: record})
	return nil
}

// Run starts the drainer pool. Returns when ctx cancels and the buffer
// drains (best-effort within a 5s grace window).
func (q *Queue) Run(ctx context.Context) error {
	for i := 0; i < q.cfg.Workers; i++ {
		q.wg.Add(1)
		go q.drain(ctx, i)
	}
	<-ctx.Done()
	q.once.Do(func() { close(q.stop) })
	q.wg.Wait()
	return nil
}

func (q *Queue) drain(ctx context.Context, id int) {
	defer q.wg.Done()
	log := q.log.With().Int("worker", id).Logger()
	for {
		select {
		case <-q.stop:
			return
		case e, ok := <-q.ch:
			if !ok {
				return
			}
			queueDepth.Set(float64(len(q.ch)))
			pubCtx, cancel := context.WithTimeout(context.Background(), q.cfg.PublishTimeout)
			if err := q.publisher.PublishTriggerFired(pubCtx, e.DevLicense, e.Record); err != nil {
				errorTotal.Inc()
				log.Warn().Err(err).Str("devLicense", e.DevLicense).Msg("audit publish failed")
			} else {
				publishedTotal.Inc()
			}
			cancel()
			_ = ctx // ctx parameter kept for symmetry with other Run signatures
		}
	}
}
