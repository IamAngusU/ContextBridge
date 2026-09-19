# Routing decisions

ContextBridge can explain a live route before work is submitted and preserve
the exact bounded evidence used when a job is assigned.

## Preview without running AI

Use an ordinary cluster job JSON file:

```bash
contextbridge route explain \
  --config /var/lib/contextbridge/config.yml \
  --file ./job.json
```

The identical long form is:

```bash
contextbridge cluster route explain --file ./job.json
```

This operation validates and scopes the request, reads the current worker
heartbeats, and returns a decision. It does **not** create a job, reserve a
worker slot, acquire a browser conversation, send a prompt, or reserve an E2EE
key. Capacity may therefore change immediately afterward.

## Explain an assigned job

After a job has been assigned:

```bash
contextbridge route explain --job job_0123456789abcdef
```

That command reads the decision stored in the same BoltDB transaction as the
assignment. A still-queued job has no durable decision yet and returns an
explicit conflict instead of reconstructing history from newer telemetry.

Add `--json` to either command for machine-readable output.

## Reading a decision

Eligible candidates have a score. Lower is preferred. Every score is the sum
of bounded weighted components for active load, queue depth, RAM pressure, CPU
and GPU pressure, VRAM headroom, browser-slot pressure, loaded-model preference,
measured VRAM fit, and explicit node preference. Zero-valued components are
omitted from the concise terminal view but remain structurally defined.

Ineligible candidates carry stable reason codes. Current codes include:

- `worker_not_connected`
- `worker_telemetry_stale`
- `worker_at_capacity`
- `provider_not_available`
- `task_not_verified`
- `model_not_available`
- `capability_not_available`
- `browser_session_not_ready`
- `browser_session_evidence_unavailable`
- `browser_slots_busy`
- `insufficient_vram`
- `group_scope_unavailable`
- `required_tag_unavailable`
- `session_affinity_node_mismatch`
- `sealed_assignment_node_mismatch`

Clients should treat unknown future codes as additional reasons, not as
permission to route around a rejection.

## Boundaries

- The relay applies the authenticated producer's group scope before producing
  a preview.
- A record contains requirements, node ID/display name, score evidence, reason
  codes, and evidence age. It does not contain prompt text, response text,
  files, browser URLs, tab titles, full session inventories, or complete
  hardware snapshots.
- At most 128 candidate records are retained. The response reports the total
  and the number omitted.
- The decision is an explanation of deterministic scheduling evidence, not a
  model-generated justification and not a quality claim about the selected AI.
- The durable record follows the job's authorization and retention. When job
  detail is pruned, its route decision is pruned too.
- E2EE still fixes the worker key before payload encryption. A preview is not an
  E2EE assignment and cannot be used as proof that the same node will be chosen.

These boundaries keep the operator view calm while preserving enough forensic
evidence to audit placement without turning telemetry or model output into an
untrusted learning loop.
