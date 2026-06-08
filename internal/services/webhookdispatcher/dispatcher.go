// Package webhookdispatcher decouples outbound webhook delivery from the
// JetStream message handler. The handler hands off a Job and returns; a
// worker pool owns the actual HTTP dispatch, state writes, audit publish,
// and the failure-count bookkeeping that drives the circuit breaker.
//
// The decoupling matters at scale. Sync delivery makes a slow receiver
// directly throttle our consumer because the JetStream message stays
// un-acked until the HTTP round-trip completes. With a worker pool, the
// consumer keeps pulling, MaxAckPending isn't consumed by a single slow
// destination, and per-pod connection pools stay warm independent of
// which replica evaluated which fire.
//
// Backpressure: when the queue is full, Enqueue returns ErrQueueFull. The
// caller (the JetStream handler) returns that error, the message is naked,
// and JetStream redelivers per its BackOff ladder. Consume_total{outcome=nak}
// climbs visibly, and once MaxDeliver is exhausted the message lands in the
// DLQ. Operators can tune workers / queue size from those signals.
//
// Pool size 0 = inline / synchronous mode. Used by single-replica deploys
// and tests that don't want the worker-pool machinery.
package webhookdispatcher

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"sync"
	"time"

	"github.com/DIMO-Network/cloudevent"
	"github.com/DIMO-Network/server-garage/pkg/richerrors"
	"github.com/DIMO-Network/vehicle-triggers-api/internal/controllers/webhook"
	"github.com/DIMO-Network/vehicle-triggers-api/internal/db/models"
	vtnats "github.com/DIMO-Network/vehicle-triggers-api/internal/nats"
	"github.com/DIMO-Network/vehicle-triggers-api/internal/services/webhooksender"
	"github.com/ethereum/go-ethereum/common"
	"github.com/nats-io/nats.go/jetstream"
	"github.com/rs/zerolog"
)

// ErrQueueFull is returned by Enqueue when no worker slot is available. The
// caller (a JetStream handler) should propagate this so the message is naked
// and redelivered. We wrap nats.ErrBackpressure so the PullLoop treats it
// as load-shed (long nak delay, no DLQ chain) rather than poison.
var ErrQueueFull = fmt.Errorf("webhook dispatcher queue full: %w", vtnats.ErrBackpressure)

// Sender delivers a single webhook to its target URL.
type Sender interface {
	SendWebhook(ctx context.Context, t *models.Trigger, payload *cloudevent.CloudEvent[webhook.WebhookPayload]) error
}

// StateRecorder persists the fire to distributed state after successful
// delivery. RecordFire is called best-effort; errors are logged.
type StateRecorder interface {
	RecordFire(ctx context.Context, triggerID, metricName string, vehicleDID cloudevent.ERC721DID, at time.Time, snapshot json.RawMessage) error
}

// AuditPublisher emits a record to the trigger-fired audit stream. The
// production implementation (auditqueue.Queue) is a bounded fire-and-forget
// queue; the raw nats client implementation is OK for synchronous mode and
// tests.
type AuditPublisher interface {
	PublishTriggerFired(ctx context.Context, devLicense string, record []byte) error
}

// FailureRepo lets the dispatcher drive the circuit breaker. Reset on
// success, increment on delivery failure; no other DB writes happen here.
type FailureRepo interface {
	ResetTriggerFailureCount(ctx context.Context, trigger *models.Trigger) error
	IncrementTriggerFailureCount(ctx context.Context, trigger *models.Trigger, failureReason error, maxFailureCount int) error
}

// Job is the unit of work flowing through the dispatcher.
type Job struct {
	Trigger    *models.Trigger
	Payload    *cloudevent.CloudEvent[webhook.WebhookPayload]
	Snapshot   json.RawMessage
	MetricName string
	VehicleDID cloudevent.ERC721DID
}

// FailureFlushInterval is how often the failure-count coalescer drains its
// in-memory accumulator into the DB. Default 1s: short enough that the
// circuit breaker reacts within the same second as the receiver outage,
// long enough that 32 workers hammering a broken receiver collapse into
// one UPDATE per trigger per second instead of one per delivery.
const DefaultFailureFlushInterval = time.Second

// Config tunes pool size and queue depth.
type Config struct {
	// Workers is the number of goroutines pulling from the queue. 0 means
	// synchronous mode: Enqueue does the work on the caller's goroutine.
	Workers int
	// QueueSize is the channel buffer. Set comfortably above Workers - if
	// it's the same size, brief bursts immediately backpressure.
	QueueSize int
	// MaxFailureCount drives the circuit breaker; mirrors the listener's.
	MaxFailureCount int
	// JobTimeout is the per-attempt wall clock cap, protecting workers from
	// a wedged receiver. Default 30s matches the http client timeout.
	JobTimeout time.Duration
	// AuditTimeout caps the detached audit publish. Default 5s.
	AuditTimeout time.Duration
	// RetryAttempts is the number of in-worker retries on a webhook
	// delivery failure (in addition to the first attempt). Default 2 (so
	// up to 3 total attempts per job). Each retry uses exponential backoff
	// starting at RetryInitialDelay. The retry loop avoids the much heavier
	// JetStream redelivery path which re-runs CEL eval + perms + state KV.
	RetryAttempts int
	// RetryInitialDelay is the backoff before the first retry. Default
	// 100ms. Each subsequent retry multiplies by 5: 100ms, 500ms, 2.5s.
	RetryInitialDelay time.Duration
	// PerHostRPS is the per-pod per-receiver send rate ceiling. Multiple
	// triggers on the same destination share the budget naturally. 0
	// disables limiting (default). Set per receiver-tolerance, e.g. 50.
	PerHostRPS float64
	// PerHostBurst is the immediate token allowance per host. Defaults to
	// PerHostRPS so a short spike doesn't queue.
	PerHostBurst int

	// FailureFlushInterval is the failure-count coalescer drain cadence.
	// 0 = DefaultFailureFlushInterval. Set to a negative value to disable
	// coalescing entirely (writes go straight to the repo, legacy
	// behaviour - only useful in tests that want immediate visibility).
	FailureFlushInterval time.Duration
}

// rateLimiter is the dispatch-side interface satisfied by hostLimiter
// (per-pod) and clusterLimiter (KV-shared). Both honour ctx cancellation
// and return nil when no limit applies.
type rateLimiter interface {
	Wait(ctx context.Context, target string) error
}

// Dispatcher owns the worker pool and the job channel.
type Dispatcher struct {
	cfg       Config
	sender    Sender
	state     StateRecorder
	audit     AuditPublisher
	repo      FailureRepo
	limiter   rateLimiter
	coalescer *failureCoalescer
	log       zerolog.Logger

	queue chan Job
	wg    sync.WaitGroup
	stop  chan struct{}
	once  sync.Once
}

// WithClusterLimiter swaps the per-pod token bucket for a cluster-shared one
// backed by the supplied JetStream KV. Returns the dispatcher for chaining.
// Falls back to the per-pod limiter (or no limit) for the same call when
// the KV is nil or PerHostRPS <= 0.
//
// Apply BEFORE Run; the limiter is consulted inline in the worker's send
// path and isn't safe to swap once jobs are flowing.
func (d *Dispatcher) WithClusterLimiter(kv jetstream.KeyValue) *Dispatcher {
	cl := newClusterLimiter(kv, d.cfg.PerHostRPS, d.cfg.PerHostBurst, asHostLimiter(d.limiter))
	if cl != nil {
		d.limiter = cl
	}
	return d
}

// asHostLimiter is a tiny helper used by WithClusterLimiter to grab the
// existing per-pod limiter (if any) as the fallback. Returns nil when the
// current limiter is something else.
func asHostLimiter(l rateLimiter) *hostLimiter {
	if hl, ok := l.(*hostLimiter); ok {
		return hl
	}
	return nil
}

// New builds a dispatcher. Call Run before Enqueue or jobs will block on a
// nil-ready queue.
func New(cfg Config, sender Sender, state StateRecorder, audit AuditPublisher, repo FailureRepo, log zerolog.Logger) *Dispatcher {
	if cfg.QueueSize < 1 {
		cfg.QueueSize = 1
	}
	if cfg.JobTimeout <= 0 {
		cfg.JobTimeout = 30 * time.Second
	}
	if cfg.AuditTimeout <= 0 {
		cfg.AuditTimeout = 5 * time.Second
	}
	if cfg.MaxFailureCount < 1 {
		cfg.MaxFailureCount = 1
	}
	if cfg.RetryAttempts < 0 {
		cfg.RetryAttempts = 0
	}
	if cfg.RetryInitialDelay <= 0 {
		cfg.RetryInitialDelay = 100 * time.Millisecond
	}
	if cfg.PerHostBurst < 1 {
		cfg.PerHostBurst = int(cfg.PerHostRPS)
	}
	hl := newHostLimiter(cfg.PerHostRPS, cfg.PerHostBurst)
	var lim rateLimiter
	if hl != nil {
		lim = hl
	}
	d := &Dispatcher{
		cfg:     cfg,
		sender:  sender,
		state:   state,
		audit:   audit,
		repo:    repo,
		limiter: lim,
		log:     log,
		queue:   make(chan Job, cfg.QueueSize),
		stop:    make(chan struct{}),
	}
	flush := cfg.FailureFlushInterval
	if flush == 0 {
		flush = DefaultFailureFlushInterval
	}
	if flush > 0 && repo != nil {
		d.coalescer = newFailureCoalescer(repo, cfg.MaxFailureCount, flush, log)
	}
	return d
}

// Run starts the worker pool and blocks until ctx cancels. On cancel the
// workers drain every job still buffered in the queue before exiting, so a
// graceful shutdown (SIGTERM during a deploy) never loses an acked-but-unsent
// webhook. The caller MUST stop feeding Enqueue before cancelling ctx - the
// main shutdown sequence cancels the JetStream pull loops first and waits for
// them to exit - otherwise a late Enqueue can race a drained worker.
//
// Shutdown ordering inside Run:
//  1. workers drain the queue and exit
//  2. THEN the failure coalescer flushes, so failure counts recorded while
//     draining are folded into the final UPDATE instead of being dropped.
//
// Residual gap: a hard crash (SIGKILL / OOM / panic) still loses jobs sitting
// in the queue - at-least-once at the ingest boundary plus async dispatch
// can't survive that without persisting the queue. Graceful shutdown is
// lossless; an ungraceful exit is bounded by JetStream redelivery only for
// messages not yet acked.
func (d *Dispatcher) Run(ctx context.Context) error {
	if d.cfg.Workers <= 0 {
		return nil // synchronous mode: nothing to run
	}
	if d.coalescer != nil {
		// Detached context: the coalescer must outlive ctx cancel so failures
		// recorded during the worker drain still get flushed. We stop it
		// explicitly via Close() after the workers have finished.
		go d.coalescer.Run(context.Background())
	}
	for i := 0; i < d.cfg.Workers; i++ {
		d.wg.Add(1)
		go d.worker(ctx, i)
	}
	<-ctx.Done()
	d.once.Do(func() { close(d.stop) })
	d.wg.Wait()
	if d.coalescer != nil {
		d.coalescer.Close()
	}
	return nil
}

// Enqueue submits a job. In synchronous mode the job runs inline. In async
// mode the job is queued; ErrQueueFull is returned when no slot is
// available so the caller can nak the JetStream message.
func (d *Dispatcher) Enqueue(ctx context.Context, j Job) error {
	if d.cfg.Workers <= 0 {
		d.process(ctx, j)
		return nil
	}
	select {
	case d.queue <- j:
		queueDepth.Set(float64(len(d.queue)))
		return nil
	default:
		queueFull.Inc()
		return ErrQueueFull
	}
}

func (d *Dispatcher) worker(ctx context.Context, id int) {
	defer d.wg.Done()
	log := d.log.With().Int("worker", id).Logger()
	log.Info().Msg("dispatcher worker started")
	for {
		select {
		case <-d.stop:
			// Graceful shutdown: drain every job already buffered before
			// exiting so acked-but-unsent webhooks are still delivered. Pull
			// loops have stopped by the time stop fires, so no new jobs
			// arrive. process() detaches the parent context, so deliveries
			// complete on their own JobTimeout even though ctx is cancelled.
			for {
				select {
				case j, ok := <-d.queue:
					if !ok {
						return
					}
					queueDepth.Set(float64(len(d.queue)))
					d.process(ctx, j)
				default:
					log.Info().Msg("dispatcher worker stopping")
					return
				}
			}
		case j, ok := <-d.queue:
			if !ok {
				return
			}
			queueDepth.Set(float64(len(d.queue)))
			d.process(ctx, j)
		}
	}
}

// process runs the full delivery + bookkeeping for a single job. Used by
// both the worker pool and the inline mode.
//
// Includes in-worker retry with exponential backoff so a transient receiver
// hiccup doesn't bounce us back to JetStream redelivery (which would re-run
// CEL eval, permission checks, KV lookups - all wasted work for what's
// already a known-good fire). After RetryAttempts retries we surface the
// last error to the caller which propagates back to PullLoop.
func (d *Dispatcher) process(parent context.Context, j Job) {
	start := time.Now()
	ctx, cancel := context.WithTimeout(detach(parent), d.cfg.JobTimeout)
	defer cancel()

	delay := d.cfg.RetryInitialDelay
	var lastErr error
retryLoop:
	for attempt := 0; attempt <= d.cfg.RetryAttempts; attempt++ {
		if attempt > 0 {
			retryTotal.Inc()
			select {
			case <-time.After(delay):
			case <-ctx.Done():
				lastErr = ctx.Err()
				break retryLoop
			}
			delay *= 5
		}
		// Per-host rate limit: blocks until the receiver's token bucket
		// allows another send. Critically, this happens INSIDE the worker
		// goroutine so other workers serving other receivers don't wait.
		if d.limiter != nil {
			if err := d.limiter.Wait(ctx, j.Trigger.TargetURI); err != nil {
				lastErr = err
				break
			}
		}
		err := d.sender.SendWebhook(ctx, j.Trigger, j.Payload)
		if err == nil {
			d.onSuccess(ctx, j)
			deliveryTotal.WithLabelValues("ok").Inc()
			deliveryLatency.WithLabelValues("ok").Observe(time.Since(start).Seconds())
			return
		}
		lastErr = err
		// Permanent errors (4xx other than 408/425/429) won't recover on
		// retry. Skip remaining attempts so we don't burn per-host rate-
		// limit tokens on a broken receiver and don't tarpit other jobs
		// waiting for this worker.
		if errors.Is(err, webhooksender.ErrPermanent) {
			break retryLoop
		}
	}
	d.onFailure(ctx, j, lastErr)
	deliveryTotal.WithLabelValues("error").Inc()
	deliveryLatency.WithLabelValues("error").Observe(time.Since(start).Seconds())
}

func (d *Dispatcher) onSuccess(ctx context.Context, j Job) {
	if j.Trigger.FailureCount > 0 && d.repo != nil {
		if err := d.repo.ResetTriggerFailureCount(ctx, j.Trigger); err != nil {
			d.log.Error().Err(err).Str("triggerId", j.Trigger.ID).Msg("failed to reset failure count")
		}
	}
	if d.state != nil {
		if err := d.state.RecordFire(ctx, j.Trigger.ID, j.Trigger.MetricName, j.VehicleDID, time.Now().UTC(), j.Snapshot); err != nil {
			d.log.Warn().Err(err).Str("triggerId", j.Trigger.ID).Msg("state recorder write failed")
		}
	}
	d.publishAudit(j)
}

func (d *Dispatcher) onFailure(ctx context.Context, j Job, err error) {
	d.log.Warn().Err(err).Str("triggerId", j.Trigger.ID).Msg("webhook delivery failed")
	if d.repo == nil {
		return
	}
	if richError, ok := richerrors.AsRichError(err); ok && richError.Code == webhooksender.WebhookFailureCode {
		// Route through the coalescer when wired so a receiver outage
		// doesn't fan one UPDATE per worker per delivery at Postgres.
		// Without it (tests, sync mode), call the repo directly.
		if d.coalescer != nil {
			d.coalescer.Record(j.Trigger, err)
			return
		}
		if failErr := d.repo.IncrementTriggerFailureCount(ctx, j.Trigger, err, d.cfg.MaxFailureCount); failErr != nil {
			d.log.Error().Err(failErr).Str("triggerId", j.Trigger.ID).Msg("failed to handle webhook failure")
		}
	}
}

// publishAudit hands the audit record to the configured AuditPublisher. The
// production publisher is an auditqueue.Queue with a bounded buffer and a
// small drainer pool, so this returns immediately with no goroutine spawn.
// Tests and sync-mode setups can pass the raw NATS client; in that case the
// caller's goroutine takes the PublishAsync hit directly.
func (d *Dispatcher) publishAudit(j Job) {
	if d.audit == nil {
		return
	}
	devLicense := common.BytesToAddress(j.Trigger.DeveloperLicenseAddress).Hex()
	record, err := json.Marshal(j.Payload)
	if err != nil {
		d.log.Warn().Err(err).Str("triggerId", j.Trigger.ID).Msg("audit: marshal failed")
		return
	}
	// Bounded by the AuditTimeout so even sync-mode setups don't deadlock if
	// the publisher wedges; queue-backed publishers reject on overflow
	// internally, observable via vehicle_triggers_audit_dropped_total.
	bgCtx, cancel := context.WithTimeout(context.Background(), d.cfg.AuditTimeout)
	defer cancel()
	if err := d.audit.PublishTriggerFired(bgCtx, devLicense, record); err != nil {
		d.log.Warn().Err(err).Str("triggerId", j.Trigger.ID).Msg("audit publish failed")
	}
}

// detach returns a context that inherits ctx's values but not its cancel.
// Used so a JetStream handler's cancellation doesn't abort the dispatcher's
// in-progress delivery + state writes.
func detach(parent context.Context) context.Context {
	return context.WithoutCancel(parent)
}

// QueueDepth returns the current queue depth. Useful for tests.
func (d *Dispatcher) QueueDepth() int {
	return len(d.queue)
}

func (d *Dispatcher) String() string {
	return fmt.Sprintf("Dispatcher(workers=%d queueDepth=%d/%d)", d.cfg.Workers, len(d.queue), cap(d.queue))
}
