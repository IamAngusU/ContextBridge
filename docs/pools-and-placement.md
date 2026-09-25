# Pools and placement

ContextBridge treats compute as a pool of independently owned workers. A
producer may be local to the relay or remote; workers connect outbound and
advertise bounded capability evidence.

## Supported topologies

| Topology | Meaning |
| --- | --- |
| 1:1 | One producer sends to one worker. |
| N:1 | Multiple scoped producers share one worker. |
| 1:N | One producer routes across several compatible workers. |
| N:N | Multiple producers share a policy-aware worker pool. |

The current relay is one durable coordination authority. ContextBridge does
not claim active multi-relay consensus or transparent cross-relay replication.
The accepted but not yet implemented HA design, including the selected
deterministic `etcd-io/raft` core and its release proof gates, is documented in
[ADR 0001](adr/0001-fenced-single-leader-consensus.md).

It exposes three deliberately separate, content-minimizing probes:

| Endpoint | Meaning |
| --- | --- |
| `/livez` | The relay process can answer HTTP. This says nothing about durable writes. |
| `/readyz` | The durable store is usable and the relay is accepting admissions. |
| `/leaderz` | This relay is the writable coordination authority. Require HTTP 200 and `writable: true`. |

Today `/leaderz` reports `mode: standalone`; it does not imply quorum or
automatic failover. Keeping leadership separate from liveness prevents a
future reverse proxy from sending mutations to a live-but-read-only follower.
During a controlled stop, liveness remains true while readiness and
writability fail closed.

The writable relay also owns a durable authority fence. Its `cluster_id`
remains stable for the lifetime of the relay database, while `leader_epoch`
increases on every relay process start. Each dispatch adds the exact epoch and
assignment generation to the durable job. Wire-v3 workers persist the highest
accepted epoch, reject an older or foreign authority before execution, and
echo the exact fence on starts, progress, results, and cancellation handling.
The relay rejects a missing or stale fence and does not let it release a newer
slot. This is a single-relay safety primitive, not a claim of replicated
consensus or automatic failover.

## Selection order

The scheduler first rejects nodes that cannot prove hard requirements such as:

- task and provider support;
- exact model, worker group, or tag;
- adapter profile and endpoint readiness;
- minimum RAM/VRAM or required GPU backend;
- artifact, visual-input, or other declared capability;
- free slot capacity and execution-policy scope.

It then ranks remaining nodes using live capacity and bounded resource
evidence. `contextbridge route explain --file JOB.json` previews the decision
without submitting provider work. The selected job stores rejection reasons
and additive score components so placement is inspectable later.

Successful jobs also build bounded relay-owned performance evidence per node
and opaque execution route. After the configured minimum number of comparable
samples, the scheduler adds a logarithmic historical-latency component to the
same score. A route averaging 1.4 seconds can therefore beat an otherwise
similar 40-second route when both are available, but history is never a hard
lock: current slots, queue depth, CPU/GPU pressure, RAM/VRAM headroom, loaded
models, affinity and recent failures remain independent inputs. A worker at
capacity is still rejected, old evidence expires, one extreme completion is
winsorized, and an unknown worker stays neutral so new hardware can be
explored rather than being permanently treated as slow.

CB also learns a bounded load curve instead of treating that route-wide speed
as fixed truth. At assignment the relay derives a coarse `idle`, `light`,
`moderate`, or `high` class from slot occupancy, queue depth, CPU/GPU pressure,
RAM/VRAM pressure and adapter occupancy, plus `warm`, `cold`, or `unknown`
model state. A class needs the same minimum number of fresh successful samples
before it can override the route-wide baseline. This lets two nodes with equal
current telemetry be compared using how each one historically behaved under
that kind of load, while keeping state bounded to twelve classes per route.
It is deliberately not a black-box model and it does not yet normalize for
prompt/token size; those remain visible limitations rather than guessed data.

The operator controls this soft behavior centrally in `config.yml`:

```yaml
cluster:
  placement:
    performance_learning: true
    minimum_samples: 3
    history_ttl_hours: 168
    latency_weight: 12
    max_latency_penalty: 60
```

These settings cannot widen permissions, capabilities, tenant scope, cost
authority, or trust. Per-job hard and soft controls remain in `requirements`,
including exact provider/model/group/tags, minimum VRAM and
`preferred_nodes`. `route explain` reports the performance sample count,
smoothed compute estimate, current load context, whether the estimate came
from that context or the route baseline, evidence age and additive
`historical_latency` score used for a decision.

Recent transient failures are relay-owned placement evidence. A matching
execution route receives a bounded soft penalty after the first failure. Route
identity is an opaque fingerprint over the provider/model/task selection and,
for adapters, the profile, reasoning, and session constraints. Independent
adapter profiles and sessions therefore cannot poison each other, while raw
session identifiers never enter routing-health records. Three consecutive
infrastructure failures within ten minutes open a 30-second circuit; repeated
failed probes increase the cooldown, capped at 15 minutes.

After cooldown, the route enters recovery probation. Ranking may select it,
but the durable assignment transaction grants exactly one in-flight probe for
the applicable global or producer scope. Concurrent jobs use another eligible
route or remain queued; they do not form a recovery stampede. A successful
probe clears the circuit. A failed probe atomically reopens it with increased
backoff. Cancelling a dispatched probe does not release its single-flight
lease by itself: the matching fenced worker result or connection teardown must
first prove that execution ended. A cancellation that wins before dispatch can
release immediately. Producer-triggered evidence is keyed by an opaque
producer scope, so
one producer cannot open or penalize another producer's route. Relay-observed
node disconnects use a separate global scope because they affect every caller.
Producer-policy, budget, artifact, cancellation, capacity, and planned-drain
failures do not poison route health. Workers cannot forge or clear this state
through a heartbeat. `route explain` exposes `failure_streak`,
`circuit_open_until`, `recovery_probation`, and the stable rejection reasons
`route_circuit_open` / `route_probe_in_flight`, without exposing provider error
text, owner scopes, or another producer's route labels.

## Draining a worker

Before maintenance or a planned shutdown, an administrator can stop new work
from entering one worker without cancelling work that already owns a lease:

```sh
contextbridge cluster node drain NODE_ID --config ./config.yml
contextbridge cluster status --config ./config.yml
# after maintenance
contextbridge cluster node resume NODE_ID --config ./config.yml
```

Drain state is durable at the relay and survives worker reconnects and relay
restarts. The scheduler exposes `worker_draining` in route explanations. The
assignment transaction rechecks the flag, so a dispatcher holding a stale
pre-drain snapshot cannot assign a job after the drain transition commits.
Draining does not migrate, cancel, or silently replay active work.

## Reservations and ambiguous loss

A relay reservation names one worker and one lease. The worker validates the
contract again before execution, and the relay accepts progress/result data
only from the current owner. If a connection disappears after assignment or
execution may have begun, the job is not silently replayed on another node.
This prefers an explicit ambiguous failure over a duplicate external action.

Retry-safe admission is separate: a producer-scoped idempotency key returns the
same durable job after a lost submit response and rejects changed content under
the same key. It does not pretend provider execution is universally
exactly-once.

## GPU and model evidence

Workers can report multiple GPUs, backends, total/free memory, utilization,
temperature, models, loaded state, and supported tasks. The scheduler can use
that evidence for requirements and ranking. ContextBridge does not split one
inference across GPUs unless the selected runtime already supports that. A
machine with no usable GPU remains a valid CPU worker when its engine and job
requirements permit it.

Use:

```sh
contextbridge cluster status --config ./config.yml
contextbridge hardware --config ./config.yml
contextbridge models --config ./config.yml
contextbridge route explain --config ./config.yml --file ./examples/cluster-job.json
```

Clock differences displayed for nodes are approximate observations based on
heartbeat receipt, not a time-synchronization guarantee.

## Prometheus-compatible metrics

Administrators and read-only observers may scrape `GET /metrics` with the
same bearer authentication as the relay API. Producers and anonymous callers
are denied. The endpoint exports only fixed-cardinality pool aggregates:
node/slot/job state, open/probation circuit counts, retained compute time, and
retained token counts. It never uses tenant, job, node, provider, model,
prompt, or error text as a metric label.

```sh
curl -fsS \
  -H "Authorization: Bearer $CONTEXTBRIDGE_TOKEN" \
  https://relay.example.net/metrics
```

The job, compute, and token gauges describe the relay's retained aggregate
state and may decrease after retention pruning; they are not lifetime billing
counters.
