# NATS JetStream Ingest Contract

This document is the contract between **producers** (DIS / device-ingest) and
**vehicle-triggers-api** for the JetStream-based ingest path. It is what a
producer team needs to publish signals and events that this service will
evaluate against user webhooks.

It applies when vehicle-triggers-api runs with `NATS_MODE=primary` (Kafka
bridges into NATS) or `NATS_MODE=exclusive` (NATS only, no Kafka).

---

## ⚠️ Webhook receivers MUST dedup on `data.webhookId`

This is the single most important thing to know if you build something that
*receives* DIMO webhooks. The pipeline guarantees **at-least-once**
delivery: the same logical fire can be delivered to your endpoint more than
once due to JetStream redelivery, replica races during cluster events, or
in-dispatcher retries on transient network failures.

The CloudEvent ID (`data.webhookId` field, also surfaced as the CloudEvent
`id` header) is **deterministic**: `sha256(triggerID || sourceID)[:16]`
where `sourceID` is the inbound signal/event's CloudEvent ID. The same
logical fire always produces the same `data.webhookId` regardless of how
many times we deliver it.

**Required receiver behavior:**

```
seen = persistent_set()                      // any KV / Redis / DB unique key
on receive(webhook):
  id = webhook.data.webhookId
  if id in seen:
    return 200                              // already processed; ack and move on
  process(webhook)
  seen.add(id, ttl=24h)                     // TTL > our redelivery window
  return 200
```

`vehicle_triggers_state_cas_conflicts_total{outcome="fallback"}` ticking on
our side is the public symptom that duplicates are reaching your endpoint.
If your endpoint isn't dedup'ing on `webhookId`, your downstream side-effects
WILL execute twice on any meaningful cluster event.

---

## Streams

The service provisions these on startup (idempotent `CreateOrUpdateStream`).
Producers only need to publish to the signal/event subjects; the service owns
the audit and DLQ streams.

| Stream | Subjects | Written by | Read by |
|---|---|---|---|
| `DIMO_SIGNALS` | `dimo.signals.>` | **producer** | triggers-api |
| `DIMO_EVENTS` | `dimo.events.>` | **producer** | triggers-api |
| `DIMO_TRIGGER_AUDIT` | `dimo.trigger.fired.>` | triggers-api | billing/usage |
| `DIMO_TRIGGER_DLQ` | `dimo.dlq.>` | triggers-api | ops (`cmd/triggers-dlq`) |

Stream names and subjects are configurable via `NATS_*` env (see `sample.env`),
but the defaults above are the contract.

## Subjects

Cardinality scales with the number of signal/event **names**, not the number
of vehicles. The vehicle identity travels in the payload, never the subject.

```
dimo.signals.<signalName>     e.g. dimo.signals.speed
dimo.events.<eventName>       e.g. dimo.events.harshBraking
```

`<signalName>` / `<eventName>` are the raw VSS names (no `vss.` prefix on the
subject). Characters illegal in NATS tokens (space, `.`, `*`, `>`, control
chars) must be replaced with `_`. The service applies the same sanitization on
its filter side, so a name containing a `.` (e.g. `powertrain.fuelLevel`)
becomes the subject token `powertrain_fuelLevel`.

Publishing one message **per signal** (single-element CloudEvent) is preferred
so consumers can filter precisely. A multi-signal envelope on a single subject
also works — the service unpacks it — but then the subject name is whichever
signal you keyed on, which muddies filtering. Prefer one publish per signal.

## Payload

Standard DIMO CloudEvent, JSON-encoded, exactly as on the Kafka topics today.

- Signals: `vss.SignalCloudEvent` (produced by `vss.PackSignals`)
- Events: `vss.EventCloudEvent` (produced by `vss.PackEvents`)

### Required CloudEvent header fields

| Field | Requirement | Notes |
|---|---|---|
| `subject` | **required** | Full ERC721 DID of the vehicle: `did:erc721:<chainId>:<contract>:<tokenId>`. The service decodes this to route to subscriptions. A bad DID fails the message (→ retried → DLQ). |
| `specversion` | `"1.0"` | |
| `type` | `dimo.signal` / `dimo.event` | |
| `time` | RFC3339 | Used for latency metrics. |
| `producer` | recommended | Surfaced in the webhook payload. |
| `source` | recommended | Surfaced in the webhook payload. |

### Signal data fields (`vss.SignalData`)

| Field | Notes |
|---|---|
| `name` | VSS signal name (e.g. `speed`). Must match the subject token (pre-sanitization). |
| `timestamp` | Signal observation time. |
| `valueNumber` / `valueString` / `valueLocation` | One set per the signal's value type. |

## Delivery semantics

- **At-least-once.** The service uses durable pull consumers with explicit
  ack. A handler failure naks the message; JetStream redelivers per the
  backoff ladder up to `NATS_MAX_DELIVER` (default 5).
- **After max deliveries**, the message is republished to
  `dimo.dlq.<original-subject>` with headers `X-Original-Subject`,
  `X-Failure-Reason`, `X-Delivered-Count`, then terminated so it stops
  redelivering. Inspect/replay with `cmd/triggers-dlq`.
- **Duplicates are possible** (at-least-once + redelivery). Webhook evaluation
  is idempotent w.r.t. cooldown: the `trigger_state` KV records the last fire
  per (trigger, vehicle), so a redelivered message inside the cooldown window
  does not re-fire.

## Audit stream (consumers: billing/usage)

Every successful webhook delivery publishes the full webhook CloudEvent
payload to:

```
dimo.trigger.fired.<developerLicense>
```

`<developerLicense>` is the developer's license address (hex, `0x...`). The
payload `data` carries `webhookId` (== the trigger ID), `assetDid`,
`metricName`, `service`, and the signal/event detail. Aggregate per developer
license for usage accounting. Publishes are async/best-effort and must never
be assumed to block delivery — treat the stream as eventually-consistent.

## ID semantics

- **Trigger ID == webhook ID.** The API returns `id` on webhook registration;
  that same value is `triggers.id` in Postgres, the `webhookId` in webhook and
  audit payloads, and the trigger-half of the `trigger_state` KV key
  (`<triggerID>.<assetDID>`).

## Versioning

Payload schema follows `vss` from `github.com/DIMO-Network/model-garage`. A
breaking change to the CloudEvent shape requires coordinating the
model-garage version across producer and this service. The subject layout
(`dimo.signals.<name>`) is independent of payload version and is not expected
to change.
