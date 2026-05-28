# vehicle-triggers-api Operations Runbook

Operator-facing procedures. Read top to bottom in an incident.

---

## Health quick check

```
GET /health
```

Returns 200 when the connection to NATS is up and every configured stream
(`DIMO_SIGNALS`, `DIMO_EVENTS`, `DIMO_TRIGGER_AUDIT`, `DIMO_TRIGGER_DLQ`)
returns a valid `Info` with a leader. Returns 503 with a JSON body listing
per-stream status when any of those probes fails.

If 503: check `nats stream report` against the cluster; usually a node lost
quorum or a stream lost a replica.

---

## Metric surfaces

Prometheus namespace: `vehicle_triggers`.

| Metric | Use |
|---|---|
| `vehicle_triggers_nats_publish_total{stream, outcome}` | Per-stream publish health (`ok` / `error`). |
| `vehicle_triggers_nats_consume_total{stream, outcome}` | Consume outcomes (`ack`, `nak`, `dlq`). A non-zero `dlq` rate means poison messages are accumulating - inspect with `triggers-dlq list`. |
| `vehicle_triggers_nats_eval_latency_seconds{stream, outcome}` | Histogram, JetStream timestamp to handler return. SLO surface. |
| `vehicle_triggers_state_cas_conflicts_total{bucket, outcome}` | Concurrent writers on the same `(trigger, vehicle)`. `retry` = resolved on retry; `fallback` = persistent, ended in unconditional Put. A steady non-zero rate means duplicate webhook fires are happening; receivers must dedup via the deterministic CloudEvent ID. |
| `vehicle_triggers_state_decode_errors_total{bucket}` | KV record JSON decode failures. Non-zero = schema mismatch or corruption. |
| `vehicle_triggers_tokenexchange_cache_total{outcome}` | Permission cache outcomes. Steady-state hit rate should be > 90%. Spikes in `miss` at autoscale events are expected. |

---

## Inspection tools

| Tool | Use |
|---|---|
| `triggers-state list / get / dump / watch` | Per-(trigger, vehicle) cooldown KV (`trigger_state` bucket). |
| `triggers-dlq list / get / replay / purge` | Dead-letter stream. Use `replay --all` after fixing the upstream cause; messages re-enter the normal evaluation path. |
| `triggers-bench` | Standalone load + latency probe. Useful for capacity tests on a fresh cluster. |

---

## Daily JetStream backup (Helm)

Set in `values.yaml`:

```yaml
backup:
  enabled: true
  schedule: "0 6 * * *"
  image: <internal-image-with-nats-and-aws-cli>
  s3Bucket: my-backup-bucket
  s3Prefix: vehicle-triggers-api/jetstream
```

The CronJob runs `nats stream backup` for each stream and KV bucket, tarballs
the result, and uploads to `s3://<bucket>/<prefix>/vehicle-triggers-backup-<ts>.tar.gz`.

`natsio/nats-box` does not include the AWS CLI. For S3 uploads in prod,
bake an image that includes both and set `backup.image`.

---

## Restore from backup

Restoring is destructive: it deletes the existing stream/KV and re-creates
from the snapshot. Make sure traffic is paused first (scale the service to
0 replicas) so the consumer isn't fighting the restore.

```sh
# 1. Pull the tarball
aws s3 cp s3://my-backup-bucket/vehicle-triggers-api/jetstream/vehicle-triggers-backup-<ts>.tar.gz .
tar -xzf vehicle-triggers-backup-<ts>.tar.gz
cd backup-<ts>

# 2. Restore each stream
for d in stream-*; do
  name="${d#stream-}"
  nats stream restore --force "$name" "$d"
done

# 3. Restore each KV (stored as KV_<bucket>)
for d in kv-*; do
  name="${d#kv-}"
  nats stream restore --force "KV_${name}" "$d"
done

# 4. Scale the service back up
kubectl scale deployment vehicle-triggers-api --replicas=<N>
```

Verify with `/health` and `triggers-state dump` / `triggers-dlq list`.

---

## Common incidents

### Webhook deliveries silently failing

1. Check `vehicle_triggers_consume_total{outcome="dlq"}` rate.
2. `triggers-dlq list` to see what's poisoned, `triggers-dlq get <seq>` for
   one record.
3. If failures are receiver-side and resolved, `triggers-dlq replay --all`.
4. If failures look like our parsing, log into a pod and inspect the body.

### CAS conflict counter rising

This means two replicas are racing on the same `(trigger, vehicle)`. The
state store is still correct (last write wins) but webhooks are firing
twice. Receivers should dedup on the CloudEvent ID. If they can't, see
PROD_HARDENING.md P1 "decouple webhook delivery from eval" - that's the
fix.

### Token-exchange cache hit rate drops

`vehicle_triggers_tokenexchange_cache_total{outcome=miss}` rate spikes
typically happen at autoscale-up (cold caches on new pods). Sustained drop
in hit rate means either:

- token-exchange-api increased its TTL → ours is still 15m default, mismatch
- subscriptions ballooned → cache is being evicted

Tune `TOKEN_EXCHANGE_CACHE_EXPIRATION` if needed.

### Latency histogram p99 climbs

Buckets are 1ms-30s. p99 > 100ms typically means slow webhook receivers
(HTTP dispatch is in-line). Mitigate: get receiver to fix; or build the P1
async dispatcher.

### NATS cluster lost a node

`/health` returns 503 with the failed stream named. JetStream auto-recovers
when the node returns. If permanent: rebalance replicas with `nats stream
edit --replicas`.

---

## Cutover procedure

Documented in PROD_HARDENING.md under "Cutover gates". Summary:

- merge with `NATS_MODE=off`
- enable in dev (`NATS_MODE=primary`)
- soak 1 week
- enable in staging
- enable in prod
- once DIS publishes natively, flip to `NATS_MODE=exclusive`
- after exclusive is clean in prod, remove Kafka deps in a follow-up PR
