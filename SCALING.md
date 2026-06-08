# vehicle-triggers-api Scaling Playbook

Companion to `PROD_HARDENING.md`. The P3 items in that document are
scaling levers we'd pull when prod metrics show we've outgrown the current
design. Each section here has:

1. **Trigger** — what observable condition justifies the work
2. **Design** — what to build
3. **Risk** — what can go wrong
4. **Out-of-scope** — what we explicitly aren't solving

These are not "do now" items. They're "do when the metric crosses the
trigger" items. Capturing them now means the future engineer has a
starting line, not a blank page.

---

## Stream sharding

### Trigger

`vehicle_triggers_nats_publish_total{stream="DIMO_SIGNALS"}` rate sustained
above **~40k msg/s** for more than 1h, **or** `nats stream report` shows
`bytes` growth outpacing storage budget.

The bench measured ~50k/s as the single-stream ceiling on replicas=3 file
storage. Plan to act when we're at 80% of that.

### Design

Shard `DIMO_SIGNALS` by signal-name prefix:

```
DIMO_SIGNALS_A   subjects: dimo.signals.a*
DIMO_SIGNALS_B   subjects: dimo.signals.b*
DIMO_SIGNALS_C   subjects: dimo.signals.c*  (etc.)
```

Or hash:

```
DIMO_SIGNALS_0   subjects: dimo.signals.0.*
DIMO_SIGNALS_N   subjects: dimo.signals.<N>.*  with DIS picking shard by hash(signalName) % N
```

Wiring on our side:

- `config.NATSSettings.SignalsStreamShards int` (default 0 = unsharded)
- `EnsureStreams` loops over shards
- `EnsureConsumer` creates one durable per shard with the appropriate
  `FilterSubjects`
- `app.CreateServers` runs one `PullLoop` per shard

DIS side: pick shard during publish. Subject convention is the contract;
hash function lives in `internal/nats/subjects.go` for both sides to share.

### Risk

- **Rebalancing during sharding.** Migrating from 1 → N shards needs a
  drain-and-restart, or a transitional "write to both" period. Plan a
  weekend window.
- **Hot shard.** If one signal name dominates traffic and lands in a
  single shard, that shard hits the ceiling first. Mitigation: split that
  shard further, or hash with a salt to spread.

### Out-of-scope

Per-tenant or per-developer sharding — that's affinity routing (next).

---

## Subject sharding for affinity routing

### Trigger

`vehicle_triggers_tokenexchange_cache_total{outcome="miss"}` rate stays
above ~5% **and** webhook delivery p99 climbs because connection pools
keep going cold.

Today the pull consumer load-balances arbitrarily across pods, so the
same `(trigger, vehicle)` fire can land on different pods. Each pod
maintains its own HTTP keep-alive pool to each receiver; pool reuse only
happens when the same pod evaluates back-to-back fires for the same
receiver. At scale this means cold connection opens dominate latency.

### Design

Hash `devLicense` (or `targetURL`) into N partition subjects:

```
dimo.signals.speed         (kept as today, for backwards compat)
+ dimo.signals.p<N>.speed   (sharded form: <N> = hash(devLicense) % shards)
```

DIS keeps publishing to the unsharded subject. A bridge inside this
service (or a separate dispatcher) re-publishes to the partition subject
based on the matched trigger's developer license. Each pod consumes only
one (or a few) partition subjects.

Result: the same pod always evaluates all fires for the same developer,
so its HTTP keep-alive pool stays warm and its permissions cache always
hits.

### Risk

- **Pod churn.** Autoscale events or rolling deploys cause cold pools
  briefly. Acceptable trade-off if rare.
- **Uneven load.** A heavy developer pins one pod. Mitigation: include
  trigger ID in the hash so a single dev with many triggers spreads.
- **Rebalancing.** Same drain-and-restart story as stream sharding.

### Out-of-scope

Consistent hashing across N pods with online rebalancing. NATS doesn't
give us that natively; if we need it we're probably looking at a
separate dispatcher service.

---

## `signal_index` KV resurrection

### Trigger

Number of distinct webhook `metricName` values exceeds ~1,000 **and**
`vehicle_triggers_nats_consume_total{stream="DIMO_SIGNALS"}` shows most
messages have no matching subscription (i.e. we're spending CPU on
payloads no one cares about).

Today's `dimo.signals.>` wildcard pulls every signal regardless of
subscription. Fine at ~30 signal names. Bad at thousands.

### Design

A KV bucket `signal_index` keyed by metric name, value = active
subscription count:

```
speed         -> 12  (12 webhooks subscribed)
fuelLevel     -> 4
deepObscure   -> 0   (no subs; consumer can skip)
```

CRUD on webhook config updates the refcount (increment on
RegisterWebhook, decrement on DeleteWebhook). The consumer rebuilds its
`FilterSubjects` list periodically (or on KV watch) from the keys with
count > 0:

```
FilterSubjects: ["dimo.signals.speed", "dimo.signals.fuelLevel", ...]
```

Subjects with no active subscribers are filtered out at the JetStream
level — we never pull them, never pay parse cost.

### Risk

- **Filter cap.** `MaxAckPending` interacts with filter-subject count;
  large filter lists can be expensive for the server. Capped at
  `FILTER_SUBJECT_CAP=2048` in config today.
- **Race on refcount.** Use KV CAS (same pattern as triggerstate) to
  avoid lost increments. At low CRUD rate this is fine.

### Out-of-scope

Vehicle-level filtering. We don't want one subject per `(metric, vehicle)`
— that's the cardinality explosion we avoided in the initial subject
design.

---

## Shared permissions cache in NATS KV

### Trigger

`vehicle_triggers_tokenexchange_cache_total{outcome="hit"}` cache hit
rate drops below ~85% steady state, **and** autoscale-up events cause
visible token-exchange-api gRPC RTT spikes for >2 minutes.

Each pod today maintains an independent 15-minute permission cache.
Cold-fill on a new pod is N grpc calls; at autoscale-up with M new pods
it's M×N redundant calls.

### Design

KV bucket `permissions` with key
`<assetDID>:<devLicense>:<sortedPrivileges>` and value
`{allowed: bool, expiresAt: time}`. TTL on the bucket is 15 minutes,
matching the in-process cache TTL.

Read path:

1. Check in-process cache → hit, return.
2. Miss → check KV → hit, populate in-process, return.
3. Miss both → gRPC to token-exchange-api, populate both.

Write to KV via `Update` on observed revision so concurrent populators
don't conflict.

### Risk

- **Stale grant after revocation.** Today: 15 min worst case (per pod
  cache). With shared KV: 15 min worst case (TTL). No regression, but no
  improvement either — revocation-aware cache invalidation is a separate
  item.
- **KV outage.** Falls back to in-process cache then gRPC; same as
  today.
- **Cost.** Adds one KV roundtrip per cache miss. At expected ~10% miss
  rate this is negligible.

### Out-of-scope

Revocation propagation — that's a token-exchange-api feature (push
invalidation), not ours.

---

## Cross-region DR

### Trigger

Recovery time objective (RTO) for the service drops below the AWS region
recovery time (typically hours), **or** a regulatory requirement mandates
multi-region failover.

### Design

**Active-passive (recommended starting point):**

```
us-east-1  (active)     us-west-2  (passive)
  +------------+           +------------+
  |  NATS      | --------> |  NATS      |
  |  3 nodes   |  mirror   |  3 nodes   |
  +------------+           +------------+
  |  vehicle-  |           |  vehicle-  |
  |  triggers  |           |  triggers  |
  |  api       |           |  api       |
  +------------+           +------------+
  |  Postgres  | --------> |  Postgres  |
  |  primary   |  replica  |  read repl |
  +------------+           +------------+
```

- JetStream stream `mirror` config replicates each stream cross-region.
- Postgres logical replication keeps webhook config in sync.
- DNS failover or Route53 health check fronts the API.
- During failover, passive becomes active. Manual confirmation step
  prevents split-brain (no automatic promotion).

**Active-active** is significantly harder: DIS must publish to both
regions, which means it owns the conflict-resolution semantics. We
don't recommend until the trigger above is firm.

### Risk

- **DIS coordination.** Both designs need DIS to know about the
  passive region.
- **Postgres replica lag.** Webhook config changes during the failover
  window may be lost. Mitigation: maintenance window for the cutover.
- **Cost.** Doubles JetStream + Postgres + pod fleet costs.

### Out-of-scope

Multi-cloud (AWS + GCP). One cloud at a time.

---

## What we deliberately don't plan for

- **Per-message ordering guarantees beyond per-subject.** JetStream
  gives ordering within a subject only. Our pipeline relies on this.
  Cross-subject ordering needs a different architecture (single
  consumer, single stream) and we've explicitly traded that for
  throughput.
- **Exactly-once webhook delivery.** Receivers must dedup on the
  deterministic CloudEvent ID (`payload.ID`). This is documented in
  `NATS_CONTRACT.md`.
- **Synchronous response to webhook receivers.** The dispatcher is
  fire-and-forget from the consumer's perspective. We do not wait for
  receiver-side processing.

---

## Reference

- Bench tool: `cmd/triggers-bench`
- Per-stream sizing math: see `charts/vehicle-triggers-api/values.yaml`
  comment near `nats:` block.
- DIS publish contract: `NATS_CONTRACT.md`
- Operational procedures: `OPERATIONS.md`
- Backlog tracking: `PROD_HARDENING.md`
