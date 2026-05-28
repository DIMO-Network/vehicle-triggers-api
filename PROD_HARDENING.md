# vehicle-triggers-api Prod Hardening Backlog

Tracking list of items surfaced during the NATS JetStream migration review.
Ordered by priority. Effort estimates are rough.

Categories:
- **Correctness**: could cause wrong webhook fires or data loss
- **Performance**: throughput / latency / resource ceilings
- **Operational**: ops can't recover, can't observe, can't tune
- **Architecture**: structural smells that block future scale
- **Scaling**: items needed when load grows past current ceiling

---

## P0 — Land before / shortly after merge

### Correctness

- [ ] **CAS-based `RecordFire`.** Two replicas racing on same `(trigger, vehicle)` both pass cooldown KV read, both fire, both `Put`. Replace `kv.Put` with `kv.Update` on observed revision (or `kv.Create` for first write), retry once on conflict, skip fire on second conflict. Effort: 1-2h. File: `internal/services/triggerstate/triggerstate.go`.
- [ ] **Deterministic webhook ID for receiver dedup.** Today `payload.ID = uuid.New().String()` regenerates per delivery, so JetStream redelivery looks like a new event to receivers. Make ID = `sha256(triggerID|assetDID|firedAtUnix)` or include stream sequence in payload. Effort: 30m. File: `internal/controllers/metriclistener/metric_listener.go::createWebhookPayload`.
- [ ] **KV decode error metric.** `lookupPreviousSignal` / `lookupPreviousEvent` silently swallow JSON decode errors and return zero-value. Add `vehicle_triggers_kv_decode_errors_total{bucket}` so silent corruption shows up. Effort: 30m. File: `internal/services/triggerevaluator/trigger_evaluator.go`.

### Performance — quick wins

- [ ] **Configure webhook HTTP Transport.** Today `webhook_sender.go` uses Go's default Transport (`MaxIdleConnsPerHost=2`). Set:
  ```go
  Transport: &http.Transport{
      MaxIdleConns:          1024,
      MaxIdleConnsPerHost:   64,
      IdleConnTimeout:       90 * time.Second,
      ForceAttemptHTTP2:     true,
      TLSHandshakeTimeout:   10 * time.Second,
      ResponseHeaderTimeout: 20 * time.Second,
      DisableCompression:    false,
  }
  ```
  Removes TLS handshake overhead at high fire rates. Effort: 30m. File: `internal/services/webhooksender/webhook_sender.go`.

- [ ] **End-to-end latency histogram.** Add `vehicle_triggers_eval_latency_seconds{outcome}` measured from JetStream `meta.Timestamp` to handler return. We have throughput counters but no SLO surface. Effort: 1h. File: `internal/nats/consumer_loop.go` + a new `metrics.go`.

- [ ] **Token-exchange cache hit/miss counters.** No way today to see whether the 15m permission cache is doing its job. Add `vehicle_triggers_tokenexchange_cache_total{outcome=hit|miss}`. Effort: 30m. File: `internal/clients/tokenexchange/cache.go`.

### Operational

- [ ] **`Settings.Validate()` called from `main`.** Catches misconfigurations at startup instead of at first failure. Validate:
  - `MaxDeliver > len(BackOff)` (already mitigated in EnsureConsumer but config-level too)
  - `AckWait < BackOff[0]` is a footgun
  - `MaxAckPending > 0`
  - `SignalsMaxAge >= AckWait * MaxDeliver` (else messages discarded mid-retry)
  - `NATS_MODE` in {`off`, `primary`, `exclusive`}
  - When `Mode != off`, `URL` non-empty
  Effort: 1-2h. File: `internal/config/settings.go`.

- [ ] **Stream/KV write canary in `/health`.** Connection alive ≠ JetStream writable. Read `js.Stream(ctx, name).Info()` and reject if `Lost != 0` or `Status != Online`. Or do a periodic noop write to a sentinel subject. Effort: 1h. File: `internal/app/app.go::CreateFiberApp`, `internal/nats/client.go`.

- [ ] **Backup automation.** Today recovery story is "you can `nats stream backup`." No CronJob, no S3 destination, no documented restore procedure. Add:
  - `cmd/triggers-backup` that snapshots streams + buckets to a configurable S3 prefix
  - Helm CronJob template running it daily
  - Restore runbook section in `NATS_CONTRACT.md` or new `OPERATIONS.md`
  Effort: half day.

---

## P1 — Land before exclusive cutover in prod

### Performance / architecture

- [ ] **Decouple webhook delivery from eval handler.** Today `SendWebhook` is synchronous to the JetStream message handler — a slow receiver holds `MaxAckPending` slots and throttles consume. Async worker pool:
  - Handler enqueues to a buffered channel + writes state + acks NATS
  - Worker pool drains channel, owns per-receiver connection pools and retry
  - On worker pool overflow, signal backpressure (return error to handler → nak)
  Trade-off: failure semantics get harder (we own retry, not JetStream). Pair with deterministic webhook ID so receivers can dedup. Effort: 1-2 days. Files: new `internal/services/webhookdispatcher/` + wire into `metric_listener.go`.

- [ ] **Audit publish fire-and-forget queue.** `PublishAsync` blocks at `MaxAckPending=4000`. We launch a 5s detached goroutine per fire — at 30k/s with 5s blocks that's potentially 150k goroutines. Replace with:
  - Non-blocking bounded channel (e.g. 50k slots)
  - Small drainer pool calling `PublishAsync` and discarding (or counting) overflow
  Audit loss > goroutine explosion. Effort: half day. File: `internal/controllers/metriclistener/metric_listener.go::publishAudit`.

- [ ] **Webhook cache distributed updates.** Today each replica polls Postgres every 5 min. CRUD propagation latency = 5 min worst case. Either:
  - **NATS pub/sub on `dimo.webhook.changed.<webhookID>`**: API handler publishes after DB write; replicas subscribe and invalidate. Simple.
  - **Watch `webhooks` KV bucket**: API handler does DB write + KV upsert. Replicas WatchAll → live updates. Reconciler covers drift.
  Effort: 1 day. Files: `internal/services/webhookcache/`, `internal/controllers/webhook/`, `internal/app/app.go`.

- [ ] **Webhook signing (HMAC).** TODO in `webhook_sender.go:74`. Sign body + timestamp with per-developer secret stored on the trigger record. Receiver verifies. Required to prevent third-party spoofing of webhook payloads. Effort: 4h. Files: `internal/services/webhooksender/`, `triggers` DB schema (new `signing_secret` column + migration).

### Operational

- [ ] **DLQ-headers include developer + vehicle context.** Today DLQ records carry `X-Original-Subject` / `X-Failure-Reason` / `X-Delivered-Count`. Adding `X-Developer-License` and `X-Asset-DID` makes triage possible without parsing payload. Effort: 30m. File: `internal/nats/bridge.go::publishDLQ`.

- [ ] **Webhook config audit trail.** Currently CRUD modifies `triggers` row in place with no history. For compliance / ops debugging, append every change to an immutable `dimo.config.changed.<webhookID>` stream. Bonus: pairs with webhook cache distributed updates. Effort: 4h.

---

## P2 — Nice-to-have / cleanup

### Architecture

- [ ] **Failure count out of DB.** `IncrementTriggerFailureCount` writes one row per delivery failure. Move to KV with rate-limited DB flush every N seconds (or just KV). Effort: half day. File: `internal/services/triggersrepo/`, `internal/controllers/metriclistener/metric_listener.go`.

- [ ] **Drop `webhooks` KV bucket OR wire it.** Provisioned in `EnsureBuckets`, never read or written. Either delete or commit to using it for distributed cache invalidation (paired with P1 webhook cache item). Effort: varies. File: `internal/nats/provision.go`.

- [ ] **Drop dead `triggersrepo` methods.** `GetLastLogValue`, `GetLastLogForMetric`, `CreateTriggerLog` no longer called by anything. Either remove or leave with a `// Deprecated:` doc string pointing at the audit stream. Effort: 30m.

- [ ] **Drop `trigger_logs` table.** After audit-stream consumer is built and a soak period proves we don't need it. Effort: 30m migration + coordination.

- [ ] **Normalize one name for trigger ID.** Audit and webhook payloads use both `webhookId` and `triggers.id` for the same value. Pick one and use it everywhere. Effort: 1h.

- [ ] **Unify "do work when X enabled" pattern.** Two styles in app.go: `bridge != nil` (interface presence) vs `settings.NATS.PrimaryMode()` (config). Pick one. Effort: 1h.

### Operational

- [ ] **Helm storage sizing guidance.** `NATS_STREAM_REPLICAS=3` default, no comment on storage per `MaxAge`. At 30k/s × 24h × ~600B = ~50GB/node/stream. Document in `values.yaml`. Effort: 30m.

- [ ] **CI smoke load test.** `cmd/triggers-bench` is manual. Add a `go test -tags=load` test that runs it briefly against a testcontainer cluster and asserts no drops at low rate. Catches wiring regressions. Effort: half day.

- [ ] **Reconciler pod for webhook cache.** Periodic full DB → KV scan that catches drift from failed dual-writes (paired with P1 webhook cache distribution). Effort: half day.

---

## P3 — When load forces it

### Scaling

- [ ] **Stream sharding.** Today one `DIMO_SIGNALS` stream caps around ~50k/s in our bench (~20k/s with sync publish + replicas=3). Shard by signal-name prefix or hash:
  - `DIMO_SIGNALS_0` (`dimo.signals.[a-h]*`), `_1`, `_2`, `_3`
  - DIS picks shard by `hash(signalName) % N`
  - Consumers fan out per shard
  Effort: 1-2 days. Requires DIS coordination.

- [ ] **Subject sharding for affinity routing.** Hash `devLicense` (or `targetURL`) into N partition subjects, run a consumer per partition pinned via the NATS consumer's filter. Same pod evaluates all fires for a given developer → its HTTP keep-alive pool to that receiver stays warm. Effort: 1-2 days. Risk: pod churn = rebalance + cold pools.

- [ ] **`signal_index` KV refcount + dynamic filter scoping.** If subscription cardinality grows past ~thousands of unique signal names, dynamically rewrite consumer `FilterSubjects` to only include subjects with at least one active webhook. We deleted this bucket; resurrect with a clear use case. Effort: 1-2 days.

- [ ] **Cross-region DR.** Single AWS region today. JetStream supports cross-region mirroring + leaf nodes. Design needed:
  - Active-passive: prod publishes single region, passive region mirrors streams, fails over on outage
  - Active-active: harder, requires DIS publishing to both
  Effort: weeks. Required when SLO demands RTO < region recovery time.

- [ ] **Shared permissions cache in NATS KV.** Cold-pod fill spikes today: 10 new pods after scale-up → 10× duplicate gRPC calls to token-exchange. Shared KV cache flattens that. Risk: stale grant after revocation. Worth it only if measured cache miss rate is bad. Effort: half day.

---

## Cutover gates

Before flipping `NATS_MODE=primary` in **dev**:
- All **P0** items done
- DIS publish-to-NATS PR ready (or accept bridge mode indefinitely)
- Backup CronJob running
- `/health` includes stream-write canary

Before flipping `NATS_MODE=primary` in **prod**:
- 1-week soak in staging clean
- Audit consumer exists somewhere (billing team)
- Latency histogram + cache-hit counters wired
- All **P1** items done

Before flipping `NATS_MODE=exclusive` in **prod**:
- DIS publishes natively, Kafka topic going empty
- 1-week soak in `primary` showing 0 nak/dlq drift
- Drop `KAFKA_*` from chart values
- Open follow-up PR to remove Kafka code paths

---

## Open questions

- What's our actual signal volume (peak msg/sec)? Current bench measures 30k/s/pod ceiling; we need to know if production is 1k or 100k.
- Who owns audit-stream consumer for billing? Stream is populated; nobody reads.
- What's the SLO for webhook delivery latency? Drives whether we need async dispatcher (P1) or current sync path is fine.
- What's the retention requirement for `DIMO_TRIGGER_AUDIT`? 90d default; finance/legal may need 7+ years (different storage entirely).

---

## Reference

- Branch: `nats-jetstream-migration`
- PR: https://github.com/DIMO-Network/vehicle-triggers-api/pull/129
- Contract: `NATS_CONTRACT.md`
- Bench: `cmd/triggers-bench`
- Inspection: `cmd/triggers-state`, `cmd/triggers-dlq`
