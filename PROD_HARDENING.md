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

- [x] **CAS-based `RecordFire`.** ~~Two replicas racing on same `(trigger, vehicle)` both pass cooldown KV read, both fire, both `Put`. Replace `kv.Put` with `kv.Update` on observed revision (or `kv.Create` for first write), retry once on conflict, skip fire on second conflict. Effort: 1-2h. File: `internal/services/triggerstate/triggerstate.go`.~~ Done: `writeWithCAS` retries once then falls back to `Put`, bumping `vehicle_triggers_state_cas_conflicts_total{bucket, outcome=retry|fallback}`. Full prevention requires receiver dedup via deterministic ID (P0-2). Race test in `tests/e2e/nats_state_cas_test.go`.
- [x] **Deterministic webhook ID for receiver dedup.** ~~Today `payload.ID = uuid.New().String()` regenerates per delivery~~ Done: `webhookID(triggerID, sourceID)` returns `sha256(triggerID|sourceID)[:16]` hex. `sourceID` = inbound CloudEvent ID from the signal/event (carried by DIS), stable across JS redelivery. UUID fallback only when source absent. Unit tested.
- [x] **KV decode error metric.** ~~`lookupPreviousSignal` / `lookupPreviousEvent` silently swallow JSON decode errors and return zero-value. Add `vehicle_triggers_kv_decode_errors_total{bucket}` so silent corruption shows up.~~ Done: `vehicle_triggers_state_decode_errors_total{bucket}` bumped from both evaluator lookup paths.

### Performance — quick wins

- [x] **Configure webhook HTTP Transport.** ~~Today `webhook_sender.go` uses Go's default Transport (`MaxIdleConnsPerHost=2`).~~ Done: cloned default with `MaxIdleConnsPerHost=64`, `MaxIdleConns=1024`, `IdleConnTimeout=90s`, `ForceAttemptHTTP2=true`. See `defaultTransport()`.
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

- [x] **End-to-end latency histogram.** ~~Add `vehicle_triggers_eval_latency_seconds{outcome}`~~ Done: `vehicle_triggers_nats_eval_latency_seconds{stream, outcome}` recorded from `meta.Timestamp` at ack/nak/dlq sites. Buckets 1ms-30s.

- [x] **Token-exchange cache hit/miss counters.** ~~No way today to see whether the 15m permission cache is doing its job.~~ Done: `vehicle_triggers_tokenexchange_cache_total{outcome=hit|miss|error}` bumped in `Cache.HasVehiclePermissions`.

### Operational

- [x] **`Settings.Validate()` called from `main`.** ~~Catches misconfigurations at startup instead of at first failure.~~ Done: `Settings.Validate()` + `NATSSettings.Validate()` enforce mode enum, KAFKA-required-when-not-exclusive, MaxDeliver/MaxAckPending/AckWait/FetchBatch/StreamReplicas >= 1, retention >= AckWait * MaxDeliver. Called from `main` after `env.LoadSettings`. Unit-tested in `settings_test.go`.
  - `MaxDeliver > len(BackOff)` (already mitigated in EnsureConsumer but config-level too)
  - `AckWait < BackOff[0]` is a footgun
  - `MaxAckPending > 0`
  - `SignalsMaxAge >= AckWait * MaxDeliver` (else messages discarded mid-retry)
  - `NATS_MODE` in {`off`, `primary`, `exclusive`}
  - When `Mode != off`, `URL` non-empty
  Effort: 1-2h. File: `internal/config/settings.go`.

- [x] **Stream/KV write canary in `/health`.** ~~Connection alive ≠ JetStream writable.~~ Done: `Client.StreamHealth(ctx)` probes each configured stream via `js.Stream(name).Info()`, reports per-stream status, no-leader detected. `/health` returns 503 when any stream lookup/info fails.

- [x] **Backup automation.** ~~Today recovery story is "you can `nats stream backup`."~~ Done: Helm CronJob templates (`backup-cronjob.yaml` + `backup-script-configmap.yaml`) run `nats stream backup` for each stream + KV bucket via the nats-box image, tarball + S3 upload. Default off; enabled with `backup.enabled=true` + `backup.s3Bucket`. Restore procedure documented in `OPERATIONS.md`. We deliberately did NOT build a Go binary - the standard `nats` CLI is the right tool for server-native snapshots.

---

## P1 — Land before exclusive cutover in prod

### Performance / architecture

- [x] **Decouple webhook delivery from eval handler.** ~~Today `SendWebhook` is synchronous to the JetStream message handler — a slow receiver holds `MaxAckPending` slots and throttles consume.~~ Done: `internal/services/webhookdispatcher` is a pluggable pool. Workers own send + state + audit + circuit-breaker bookkeeping. `Enqueue` returns `ErrQueueFull` on overflow so the JetStream handler naks and JetStream retries. Default pool size 32 / queue 4096 via `NATS_DISPATCHER_WORKERS` / `NATS_DISPATCHER_QUEUE_SIZE`. `WithDispatcher` on the listener swaps the sync path out; sync path retained for the Kafka-only legacy mode. Metrics: queue depth gauge, queue_full counter, delivery_total+latency histogram.

- [x] **Audit publish fire-and-forget queue.** ~~`PublishAsync` blocks at `MaxAckPending=4000`. We launch a 5s detached goroutine per fire — at 30k/s with 5s blocks that's potentially 150k goroutines.~~ Done: `internal/services/auditqueue` is a bounded buffer + small drainer pool. `Submit` drops on overflow (non-blocking). Drops surface via `vehicle_triggers_audit_dropped_total` — alarm on this. Adapter implements the `webhookdispatcher.AuditPublisher` interface so the dispatcher's `publishAudit` no longer spawns a goroutine. Default `NATS_AUDIT_WORKERS=4`, `NATS_AUDIT_QUEUE_SIZE=16384`.

- [x] **Webhook cache distributed updates.** ~~Today each replica polls Postgres every 5 min.~~ Done: `internal/services/cachebroadcast` publishes change events on plain NATS subject `dimo.cache.webhook.changed`. `WebhookCache.ScheduleRefresh` publishes; receiver side calls `ScheduleRefreshSilent` to avoid echo loops. App wires the notifier on `WebhookCache` and subscribes in `CreateServers` when NATS is enabled. CRUD controllers untouched. 5-min poll stays as reconciliation safety net.

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
