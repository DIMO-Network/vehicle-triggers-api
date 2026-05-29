# vehicle-triggers-api Prod Hardening Round 2

Findings from a fresh systems-engineer review after PROD_HARDENING.md items
landed. Items A through S. Same structure as the first backlog: priority,
trigger, effort.

Goal: clear A through S so this branch is genuinely production-ready beyond
"all tests pass." The first review's items were about wiring + observability;
this round is about correctness gaps and operational sharp edges the first
pass left behind.

---

## P0 — must land before exclusive cutover

These are real bugs or readiness gaps that will bite on first cutover attempt.

### Correctness

- [x] **B. State TTL silently breaks long cooldowns.** `NATS_TRIGGER_STATE_TTL=168h` (7d). A trigger with `cooldownPeriod > 7d` loses its record after TTL → fires again as if first-time. Add to `Settings.Validate`:
  ```
  TriggerStateTTL >= 2 * max(triggers.cooldown_period)
  ```
  Or query DB at startup and assert. Without this, configuration drift becomes a silent correctness bug. Effort: 1h. File: `internal/config/settings.go`.

- [x] **C. `ScheduleRefresh` from PermissionDenied broadcasts.** `metric_listener.go::processSignalWebhook` calls `webhookCache.ScheduleRefresh` after a permission-denied → DeleteVehicleSubscription. With cachebroadcast (P1-3) this now publishes a NATS message every time. At 30k signals/sec with a misconfigured developer, thousands of broadcasts per second across all replicas. Debounce inside ScheduleRefresh doesn't help because each call is a fresh NATS publish, only the local rebuild is debounced. Fix: subscription deletion doesn't need a full cache rebuild; it needs targeted invalidation OR rate-limit the broadcast at the cachebroadcast.NATSNotifier level. Effort: 30m. File: `internal/services/cachebroadcast/cachebroadcast.go` + `internal/controllers/metriclistener/signal.go`.

- [x] **D. `ErrQueueFull` nak counts toward `MaxDeliver`.** Dispatcher backpressure → handler returns ErrQueueFull → nak → JetStream increments NumDelivered → 5 naks land message in DLQ. Transient saturation looks like poison. Fix: in `cmd/vehicle-triggers-api::RunNATSConsumer` callback (or `PullLoop`), unwrap the handler error; if it's `webhookdispatcher.ErrQueueFull`, call `NakWithDelay(longer)` and do NOT count toward MaxDeliver. JetStream doesn't natively support "don't count this nak" so we either:
   - Implement a separate redelivery counter in-process and bypass the consumer's MaxDeliver
   - Use a longer NakWithDelay and accept slower retries
  Effort: 1h. File: `internal/nats/consumer_loop.go`.

### Operational

- [x] **I. `/health` synchronous stream probes can blow K8s probe timeout.** 4 streams × 500ms timeout each = up to 2s. K8s `probe.timeoutSeconds=3`, `periodSeconds=5`. At cluster degradation, /health itself becomes a tarpit and the probe times out → pod marked unready while it's actually fine. Fix: probe streams in parallel with `errgroup`; cap total at 1s. Effort: 30m. File: `internal/nats/client.go::StreamHealth`.

---

## P1 — land before primary cutover in prod

### Performance

- [x] **F. No inline webhook retry.** Single HTTP attempt → fail → return error → JS redelivers → re-evaluates from scratch (parse + CEL + perms + KV). Wasteful. Worker-internal retry loop with capped backoff (3 tries, 100ms/500ms/2s) handles the common transient receiver hiccup without re-running eval. Trade-off: failure semantics live with us, not JS. Pair with deterministic webhook ID (already shipped). Effort: 4h. File: `internal/services/webhookdispatcher/dispatcher.go`.

- [x] **G. KV write throughput not benchmarked.** At 30k fire/sec we do 60k KV writes/sec (`trigger_state` + `signal_history`). The cluster bench measured stream publish + pull throughput, not KV writes. KV is its own JetStream-backed stream with its own ceiling. Extend `cmd/triggers-bench` with a `-kv-writes` mode or write a separate kv-bench. Effort: half day. Files: `cmd/triggers-bench/main.go` or new `cmd/triggers-kvbench/`.

- [x] **H. Stream `MaxBytes` not set.** Storage bounded only by `MaxAge`. One pathological signal name dominating traffic can consume the whole disk before age expires. Add `MaxBytes` per stream with `Discard: DiscardOld`. Defaults: configurable via `NATS_*_MAX_BYTES`. Effort: 30m. File: `internal/nats/provision.go`.

### Architecture

- [x] **Q. No global rate limiting to receivers.** N pods × 32 workers each = up to 320 concurrent POSTs to one receiver. If they rate-limit us at 10 RPS, we burn through `MaxFailureCount` on every fire. Per-receiver token bucket, ideally shared across replicas via a KV semaphore. Without shared state: per-pod bucket + accept that aggregate rate is `pods * bucket_rate`. Effort: 1 day. Files: new `internal/services/ratelimit/` + dispatcher worker integration.

### Observability

- [x] **N. Trace propagation via JS message ID.** Following one fire from `dimo.signals.speed` → CEL eval → dispatcher → receiver across multiple pods needs a span ID. JS message ID is natural. Thread through: handler logs include `js_msg_id`, dispatcher logs include it, outbound webhook gets `X-DIMO-Trace-ID` header. Receiver-side trace integration optional. Effort: half day. Files: `internal/nats/consumer_loop.go`, `internal/services/webhookdispatcher/dispatcher.go`, `internal/services/webhooksender/webhook_sender.go`.

### Test coverage

- [x] **O. Replica=3 testcontainer NATS in CI.** All testcontainer tests use single-node. Real bugs (replica election, lost writes during failover) won't surface. Add a `tests/e2e/nats_cluster_test.go` that spins 3 NATS containers on a podman network (we did this manually for the cluster bench; mechanize it). Effort: 1 day.

---

## P2 — cleanup that we should not ship without

### Operational

- [x] **E. Audit-side PublishAsync block telemetry.** Our queue is bounded; but once a worker calls `js.PublishAsync`, that can **block** when JetStream's own `max_pending=4000` is exhausted. Worker goroutines park. We don't track time-blocked. Add a histogram `vehicle_triggers_audit_publish_blocked_seconds`. Effort: 1h. File: `internal/services/auditqueue/auditqueue.go`.

- [x] **J. `webhookcache` 5-min refresh goroutine leaks after shutdown.** `range ticker.C` with no ctx check; never exits. Minor but signals shutdown discipline gap. Fix: select on ctx.Done() inside the loop. Effort: 15m. File: `internal/services/webhookcache/webhook_cache.go` (or actually `internal/app/app.go::startWebhookCache`).

- [x] **K. Decode error counter without payload context.** `vehicle_triggers_state_decode_errors_total` ticks; nothing logs the malformed body. Add a sampled debug log (1 in 100) with first 200 bytes. Effort: 30m. Files: `internal/services/triggerevaluator/trigger_evaluator.go::lookupPreviousSignal` / `lookupPreviousEvent`.

### Security

- [x] **L. No signing-secret rotation.** Lose the secret → must delete + recreate webhook → vehicle subscription list gone. Add `POST /v1/webhooks/:id/rotate-secret` returning the new secret once, marking old secret as expired. Optional: dual-secret window (old valid for N minutes after rotation). Effort: 4h. Files: `internal/controllers/webhook/webhook_controller.go`, `internal/services/triggersrepo/triggersrepo.go`.

- [x] **M. Signing secret stored plaintext in Postgres.** DB compromise = every webhook secret compromised at once. Two paths:
   - Envelope encryption via AWS KMS: encrypt at write, decrypt at read (small per-call latency)
   - Store in AWS Secrets Manager per trigger ID, DB holds only a reference
  Picking the cheaper of the two. Effort: half day (KMS) to 1 day (Secrets Manager). Files: `internal/services/triggersrepo/triggersrepo.go`, new `internal/services/secrets/` package.

### Correctness

- [x] **A. CAS protects state writes, not fire decisions.** Two evaluators on different replicas both pass `lookupLastFireTime` (zero), both fire, both reach RecordFire. CAS conflict is recorded but both webhooks already sent. Receiver dedup via deterministic ID (P0-2 shipped) is the only real protection. Reality: this isn't a code fix, it's a documentation + contract item:
   - Move the receiver-dedup callout from buried in NATS_CONTRACT.md to the TOP, in bold
   - Add an example of how to dedup in the receiver doc
   - Add a "we tested duplicates happen" e2e test asserting that two parallel writers produce two webhook deliveries with identical IDs
  Effort: 2h. Files: `NATS_CONTRACT.md`, new `tests/e2e/nats_duplicate_fire_test.go`.

---

## P3 — known issues, low cost-of-deferral

- [x] **P. `payload.Source = "vehicle-triggers-api"` hardcoded.** TODO in `metric_listener.go` says "should be 0x of the storageNode." Receivers verifying CloudEvent provenance can't tell which DIMO node served the event. Blocked on storage-node identity being available at runtime (which it isn't yet). Effort: depends on upstream; keep TODO visible.

- [x] **R. Audit drops are silent billing miscounts.** Counter exists; no alert. Add a Prometheus alert rule template in `OPERATIONS.md`:
  ```
  rate(vehicle_triggers_audit_dropped_total[5m]) > 0
  ```
  fires at any drop. Effort: 30m. File: `OPERATIONS.md` + the chart's ServiceMonitor.

- [x] **S. Bridge mode does 2N JetStream ops per Kafka message.** Acceptable transitional cost; defined cutover acceptance criteria here so it doesn't become permanent:

  - **Trigger A**: `NATS_MODE=primary` runs in prod >= 90 days.
  - **Trigger B**: aggregate publish + ack JetStream operations driven by the bridge cost more than 30% of the cluster's measured budget (compare `rate(vehicle_triggers_nats_publish_total{stream="DIMO_SIGNALS"})` × 2 against published cluster ceiling).

  When either trigger fires, escalate to DIS team for native-publish cutover. The work on our side is just flipping `NATS_MODE=exclusive`; the gating action is DIS publishing to `dimo.signals.>` directly. Documented this checkpoint in `SCALING.md` cross-region section neighborhood.

---

## Cutover gates (revised)

Before flipping `NATS_MODE=primary` in **dev**:
- All **P0** items in this doc (B, C, D, I) done

Before flipping `NATS_MODE=primary` in **prod**:
- 1 week soak in staging clean (cas_conflicts ≤ baseline, queue_full = 0, audit_dropped = 0, dlq = 0)
- All **P1** items done
- Receiver-dedup contract explicitly communicated to the largest 3 consumers

Before flipping `NATS_MODE=exclusive` in **prod**:
- DIS publishing natively to NATS
- 1 week soak in `primary` clean
- All **P2** items done

---

## Metric reference (already shipped — for trigger conditions)

| Metric | Watch for |
|---|---|
| `vehicle_triggers_state_cas_conflicts_total` | non-zero rate → replica race; tells customers to dedup |
| `vehicle_triggers_state_decode_errors_total` | non-zero → schema mismatch / corruption |
| `vehicle_triggers_dispatcher_queue_full_total` | non-zero → bump workers / queue / scale out |
| `vehicle_triggers_audit_dropped_total` | non-zero → billing data loss; alert |
| `vehicle_triggers_nats_consume_total{outcome="dlq"}` | non-zero → poison or D-class queue-full nak chain |
| `vehicle_triggers_nats_eval_latency_seconds` p99 | SLO surface |
| `vehicle_triggers_tokenexchange_cache_total{outcome="hit"}` rate | < 85% → permissions cache cold; consider shared KV cache |

---

## Reference

- Previous backlog: `PROD_HARDENING.md` (24 done + 5 deferred with triggers)
- Scaling levers: `SCALING.md`
- DIS contract: `NATS_CONTRACT.md`
- Ops runbook: `OPERATIONS.md`
- PR: https://github.com/DIMO-Network/vehicle-triggers-api/pull/129
- Branch: `nats-jetstream-migration`
