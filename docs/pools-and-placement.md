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

## Optional reserved interactive capacity

The relay can protect slots on workers in an explicitly named group with a
matching task/provider capability. This is a numeric-priority capacity slice
of #116, not a new producer-selectable QoS class. Omission disables it and
preserves existing placement and credential defaults. For example:

```yaml
cluster:
  interactive_capacity:
    min_priority: 80
    scopes:
      - group: voice
        task: generation
        provider: ollama
        slots: 1
        borrow_idle: false
```

Configure producer `max_priority` ceilings below 80 for ordinary producers
and at least 80 for producers authorized to use interactive capacity. Existing
uncapped credentials retain their historical ability to request priority 100;
enabling a reserve does not silently rewrite their authority.

Scopes select **workers**, not a separate execution pool: every lower-priority
job competing on a matching worker is subject to its reserve. Interactive
priority jobs must still pass all normal placement, trust, owner, session,
model, hardware and credential gates. Scopes require a group and task; provider
is optional. Matching overlapping scopes take the maximum slot count, capped
at worker concurrency, and borrowing requires unanimous opt-in across those
scopes. A one-slot worker with one protected slot serves only interactive
priority work until borrowing is enabled. No global hidden slot is introduced.

With `borrow_idle: false`, lower-priority executions cannot exceed
`max_concurrent - reserved_slots`. Interactive executions occupy reserved
capacity first, allowing lower-priority work to use the remaining slots.
The relay counts live reservations, including executions whose durable job
has timed out, until their fenced result or connection teardown releases them.

With `borrow_idle: true`, lower-priority work may use all slots when the
dispatch snapshot proves there is no compatible queued interactive demand.
The relay examines the priority-ordered durable queue projection independently
of the rotating dispatch window. It inspects at most 4096 interactive entries;
an incomplete scan disables borrowing for that pass. Matching interactive
candidates supplement the rotating window, retaining numeric priority order
and producer rotation within each tier. Occupied adapter counters and GPU
memory do not erase demand; actual placement still requires current readiness
and free hardware capacity. Explicit encrypted worker bindings and unsigned
jobs on certified pool workers are respected. Demand may conservatively retain
a reserve while other placement gates, such as a circuit, prevent dispatch.

An arrival after the snapshot is observed on the next dispatch pass. Reclaim
stops new lower-priority assignments above the ordinary capacity limit. It
never cancels, kills, migrates or replays an already running borrower. A newly
arriving interactive job can therefore wait for a borrower to finish; idle
borrowing does **not** guarantee bounded interactive latency. Use protected
capacity and suitable worker concurrency for that headroom. The live slot
gate checks the limit again under its lock; dispatch passes are serialized.

`cluster route explain --file job.json` forwards the job's priority (default
0), enforcing the existing credential ceiling. The same preview and durable
assignment evidence expose only `reserved_slots`, `reservation_outcome`
(`protected`, `borrow_idle`, `reclaim`, `demand_unknown`, `interactive`), and
the stable placement rejection reason `interactive_capacity_reserved`. They
do not expose queued owner names, job IDs, payloads, or queue-demand counts.
E2EE reservation creation keeps its existing binding contract; capacity is
enforced when the promoted durable job is dispatched.

Still separate in #116: authorized named QoS classes and required-class
admission failures; aging or bounded service budgets against starvation;
per-owner/tenant/producer interactive concurrency quotas; class receipts,
queue-delay evidence and fixed-cardinality QoS metrics. This slice does not
claim those guarantees and does not introduce running-job preemption.

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
It is deliberately not a black-box model. Placement itself does not normalize
by prompt/content size: hard eligibility and this soft routing curve remain
independent from the ETA-only workload profiles described below.

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

The same successful local evidence can produce a transparent advisory runtime
range for an already assigned job:

```sh
contextbridge cluster estimate JOB_ID
contextbridge cluster estimate JOB_ID --json
```

Each route and load profile retains at most 32 recent successful execution
durations. A user-facing estimate requires at least five fresh comparable
samples (or the operator's higher `minimum_samples` value), reports total
p50/p90 rather than one average, and is labelled non-authoritative. While a
job runs, CB recomputes remaining time from only historical runs whose total
duration is still greater than the authoritative elapsed time. It therefore
does not count down from a fixed deadline; when too few longer comparisons
remain, it reports uncertainty, and after the typical range it withdraws the
remaining-time range entirely.

Failed, cancelled and ambiguous work never enters this successful-duration
distribution. The API projection contains no route key, prompt, result,
tenant name or model label, and the public node list still redacts all
relay-owned route evidence. Disabling `performance_learning` disables both the
soft placement history and this advisory projection.

The estimate first tries a separate, ETA-only workload profile. The relay
derives a coarse class from facts it already owns: clear or sealed payload byte
bucket, input-image count/byte buckets, assignment load, and model warmth. It
does not inspect or retain prompt/result content and does not relabel bytes as
tokens. A configured pipeline child also includes an opaque digest of its
stable operator-owned pipeline/step identity, so repeated steps learn their
own local duration distribution without persisting those names in performance
state. Each route retains at most 16 such profiles and each profile at most 32
fresh successful samples. Selection falls back in order from pipeline-step +
workload/load evidence, to route + workload/load, to the existing route/load
and route-wide distributions. These profiles never affect eligibility,
policy, trust or placement scoring.

Measured token-count normalization remains future work because an active job
does not necessarily have authoritative token counts yet. Unknown facts stay
unknown instead of being inferred from text length.

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
