<!-- SPDX-License-Identifier: Apache-2.0 -->

# ContextBridge compatibility boundaries

ContextBridge is intended to be a neutral execution layer between callers and
the AI resources an operator chooses to connect. Compatibility is therefore a
set of narrow, versioned contracts—not a claim that two products share an
implementation, vendor, security posture, or model quality.

```text
applications · users · agents · MCP clients
                     |
         ContextBridge Job Contract v1
                     |
       policy · placement · leases · evidence
                     |
local engines · APIs · workers · resource packs · adapters
```

The contracts below already exist in the public core and can be checked
without sending an inference request.

## Compatibility surfaces

| Surface | Stable identifier or boundary | Evidence |
| --- | --- | --- |
| Producer job input | `contextbridge.job.v1` | Published JSON Schema plus relay dry-run validation |
| Producer execution identity | `relay_assigned_producer_job_ids_v1`, `job_id.relay_assigned` | Ordinary producer IDs are relay-generated; retry reconciliation uses a producer-scoped `Idempotency-Key`; a sealed submission may echo only its one-time reservation ID |
| Relay behavior | `contextbridge.protocol-manifest.v1`, current wire version | Machine-readable protocol manifest and Relay Conformance v1 report |
| Relay management API | OpenAPI 3.1 at `GET /v1/cluster/openapi.json`, `credential_identity_v1`, `observer_scopes_v1`, `cursor_job_history_v1` | Authenticated contract discovery, effective credential identity, credential-bound subject/tenant visibility, and filter-bound pagination |
| Local configuration management | `contextbridge.config-management-schema.v1`, `contextbridge.managed-config.v1` | Loopback plus bearer authorization, machine-readable ranges/enums, redacted editable YAML, strict dry-run, revision conflict protection, atomic replace, and explicit restart result |
| Live operator timelines | `bounded_execution_event_sse_v1`, `authoritative_job_events_v1`, `authoritative_pipeline_events_v1` | Authenticated bounded SSE, durable event sequence, retention-gap control, periodic reconnect, and point-visibility enforcement |
| Relay proxy probes | `relay_role_health_v1` | Separate `/livez`, `/readyz`, and `/leaderz`; current mode is explicitly `standalone` |
| Assignment authority | `assignment_fencing_v1`, wire v3 | Stable cluster identity, monotonic relay epoch, and exact per-assignment generation echoed by workers |
| Worker advertisement | `contextbridge.worker-conformance.v1` | Worker Conformance v1 report over live bounded evidence |
| Planned worker maintenance | `durable_worker_drain_v1`, `node.draining` | Admin-only durable admission state plus assignment-transaction recheck |
| Failure-aware placement | `failure_aware_routing_v1`, `routing_recovery_probation_v1`, `route_circuit_open` | Relay-owned bounded failure streak, opaque execution-route and producer isolation, global node-failure scope, cooldown, durable single-flight recovery probe, and auditable route evidence |
| Performance-aware placement | `performance_aware_routing_v1`, `load_context_performance_routing_v1` | Bounded relay-owned successful-runtime EWMA per node and opaque route, coarse live-load/model-warmth curves with route-baseline fallback, minimum samples, expiry, outlier limiting, operator-controlled soft weight, and auditable score evidence |
| Historical runtime estimate | `historical_runtime_estimates_v1`, `workload_normalized_runtime_estimates_v1`, `contextbridge.historical-runtime-estimate.v1` | At most 16 opaque ETA-only workload/step profiles per route and 32 recent successful durations per profile, fresh-sample threshold, p50/p90 totals, elapsed-conditioned remaining range, explicit uncertainty, and producer-scoped content-minimized API |
| Offline LAN trust | `secure_offline_lan_pinning_v1`, `identity_preserving_lan_relocation_v1`, LAN join bundle v1 | Relay-owned TLS leaf identity, explicitly transferred certificate/SPKI pin, persistent worker binding, same-key live relocation proof, and the same authenticated HTTP/WebSocket protocol without Internet PKI |
| Pool metrics | `prometheus_metrics_v1`, `GET /metrics` | Authenticated fixed-cardinality aggregates with no tenant, job, node, provider, model, prompt, or error labels |
| Producer resource governance | `producer_resource_governance_v1`, `capacity.owner_hourly_jobs_full` | Token-bound queue, hourly job, provider and local-egress ceilings; durable fixed-window accounting |
| Producer-required E2EE | `producer_required_e2ee_v1`, `privacy.e2ee_required` | Token-bound fail-closed rejection of cleartext job and unsupported cleartext pipeline admission; payload confidentiality only, not coordination anonymity |
| Out-of-tree adapter | `contextbridge.adapter.v2`, `contextbridge.adapter-conformance.v1` | Scoped principal, endpoint capability, per-generation lease capability, and a free self-run black-box conformance report; no certification is implied |
| Portable resource pack | `.contextbridge-pack.json`, `schema_version: 1` | Bounded discovery and identity validation; discovery is not execution approval |
| Runtime lifecycle ownership | `runtime.engines.*.lifecycle_owner` | External APIs and externally started runtimes are observed but never stopped by CB; only a llama.cpp process actually started by CB reports `contextbridge` ownership |
| RAG embedding-space identity | `rag_embedding_space_identity_v1`, `contextbridge.embedding-space.v1` | Every stored vector is bound to provider/runtime/model, revision evidence, dimensions, normalization, similarity and hashed query/passage preprocessing; cross-space search fails closed |

The runtime protocol manifest publishes exact contract versions, feature IDs,
stable admission/runtime failure-code catalogs, and hard limits. Consumers
should inspect it instead of inferring support from a product name or version
number.

```sh
contextbridge cluster protocol --config ./config.yml --json
```

The Job Contract v1 schema is published at
[`schemas/job-contract-v1.schema.json`](schemas/job-contract-v1.schema.json).
Relay validation uses the production admission and policy path but creates no
job:

```sh
contextbridge cluster contract validate \
  --config ./config.yml \
  --file ./examples/cluster-job.json \
  --json
```

## Conformance checks

Relay Conformance v1 verifies protocol identity, Job Contract v1 support,
required feature declarations, stable error catalogs, hard limits, one valid
dry run, rejection of an unsupported contract version, and closed-schema
rejection of an unknown field. It submits no job.

```sh
contextbridge cluster conformance relay \
  --config ./config.yml \
  --json > relay-conformance.json
```

Worker Conformance v1 checks the relay boundary and current bounded evidence
for connected workers: pinned identity, agent version, heartbeat freshness,
clock/capacity bounds, provider/task declarations, automatic-route
consistency, model-capability evidence, hardware bounds, adapter inventory,
and accounting invariants. It also submits no job.

```sh
contextbridge cluster conformance worker \
  --config ./config.yml \
  --json > worker-conformance.json
```

An isolated, provider-free durability proof is also built in:

```sh
contextbridge cluster conformance resilience --json > resilience-proof.json
```

It verifies a bounded set of restart, idempotency, ambiguity, fencing and
terminal-result invariants against temporary local stores. Its report is not a
claim of network, provider or multi-relay HA validation; see
[`field-validation.md`](field-validation.md) for the evidence boundary.

Adapter Conformance v1 launches an explicit out-of-tree adapter against an
isolated loopback fake core and emits no privileged credential. It probes
terminal authentication and HTTP failures, expired-lease handling, the scoped
lifecycle, endpoint capability renewal, claim ordering, cancellation
observation, replacement generation/capability fencing, and a deliberately
ambiguous completion:

```sh
contextbridge adapter conformance \
  --adapter /absolute/path/to/adapter \
  --arg run \
  --profile example \
  --profile-file ./safe-profile.json \
  --job-file ./side-effect-free-job.json \
  --json > adapter-conformance.json
```

The supplied job must be side-effect-free and safe to execute repeatedly; the
current suite can execute it four times. The harness does not sandbox the
executable or prove model/provider quality. Its report schema is
[`schemas/adapter-conformance-v1.schema.json`](schemas/adapter-conformance-v1.schema.json),
and its full boundary is documented in [`adapters.md`](adapters.md).

A passing report is point-in-time evidence for the exact endpoint, worker,
configuration, and versions tested. It is not a permanent badge, security
audit, performance result, or endorsement by the ContextBridge project.

The optional [signed verification format](verification.md) can bind reviewed
reports to exact product bytes, scope, issuer, and a validity window. It does
not replace these free self-run commands.

## Accurate compatibility statements

Prefer a statement that names the surface and version:

- `Supports ContextBridge Job Contract v1` when a producer emits requests that
  validate against the published schema and relay dry-run path.
- `Passes ContextBridge Relay Conformance v1` when the unmodified conformance
  command exits successfully; publish the JSON report and tested version.
- `Passes ContextBridge Worker Conformance v1` when the worker command exits
  successfully; publish the JSON report and evidence time.
- `Passes ContextBridge Adapter Conformance v1` when the unmodified command
  exits successfully; publish the JSON report, exact adapter version, tested
  ContextBridge version, profile scope, and evidence time. Do not call the
  adapter certified or endorsed.

The bare phrase `ContextBridge-compatible` is underspecified. Accompany it
with the supported surface, version, tested ContextBridge release, and any
known limitation. Honest compatibility statements are allowed by the project
identity policy; the logo must not be used as a certification mark or imply an
official relationship.

## Versioning and independence

Compatibility identifiers are exact. During the pre-1.0 period, callers must
not assume that a product version alone proves protocol compatibility. A
future breaking contract requires a new contract or conformance identifier;
the protocol manifest remains the runtime source of truth.

Security tightening within the pre-1.0 Job Contract v1 removed ordinary
producer-selected execution IDs from relay admission. Integrations already
following the reference flow—omit `id`, persist the accepted response ID, and
reuse an exact producer-scoped `Idempotency-Key` after an ambiguous submit—do
not change. The manifest feature and stable admission code make this boundary
machine-readable.

The schema, this document, adapter contract, and examples are licensed under
Apache-2.0 so independent software can implement these boundaries. The relay,
worker, scheduler, engines, and CLI remain part of the AGPL-covered core. See
the [licensing boundary](../LICENSING.md) and
[project identity policy](../TRADEMARKS.md).

Compatibility does not prove that a model is truthful, that advertised
hardware is dedicated to a job, that a deployment is secure, or that a
provider will remain available. ContextBridge continues to enforce policy,
lease ownership, result limits, and evidence checks after compatibility has
been established.
