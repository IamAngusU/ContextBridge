# Architecture

ContextBridge separates four roles:

1. **Producer** submits a bounded job and reads its result.
2. **Relay** authenticates, queues, schedules, and records authoritative state.
3. **Worker** advertises bounded capabilities and executes assigned work.
4. **Engine or adapter** performs one selected operation on the worker.

Workers initiate outbound connections. A producer never receives a worker's
local credential or direct filesystem access. The relay sees scheduling
metadata. For an E2EE job, prompt and result payloads can remain opaque to the
relay, but job identifiers, timing, routing requirements, worker assignment,
cost evidence, sizes, and other coordination metadata are not described as
secret. ContextBridge does not claim that the relay is zero knowledge.

Scheduling follows this order:

```text
validate request
  -> enforce tenant and execution policy
  -> filter by hard capabilities
  -> reserve cost and one worker lease
  -> dispatch once
  -> verify output and artifacts
  -> persist receipt and terminal state
```

`max_attempts` bounds dispatch generations, not arbitrary provider retries.
The only automatic retry classes are a worker capacity refusal or worker
shutdown refusal received while the authoritative job state is still
`assigned`, before any `started` message or execution evidence. Adapter jobs
are excluded because their endpoint/session state is not interchangeable.
An ambiguous post-dispatch failure is terminal because the engine may already
have performed the work.

## State authority

- The relay owns queue, assignment, cancellation, and terminal job state.
- The worker owns local runtime and hardware observations.
- Engines own model execution but do not define job success.
- ContextBridge verifies output contracts, artifact bytes, and digests.
- Adapters provide bounded endpoint capability and completion evidence; the
  core does not embed adapter-specific logic.

## Optional surrounding layers

**Connect, do not replace.** ContextBridge remains fully usable on its own.
Existing gateways, runtimes, orchestrators, observability systems, storage
systems and application layers may retain their responsibilities when that
does not weaken the explicit CB execution contract. Authority is
domain-specific: CB claims authority and evidence only for boundaries it
actually controls.

ContextBridge does not require a separate API gateway, model router, UI or MCP
host. Those components may be placed around CB when they already solve a useful
operator problem; they do not replace CB's authority model.

```text
direct:    application -> ContextBridge -> execution resource
layered:   application -> optional gateway -> ContextBridge -> execution resource
native:    orchestrator -> CB job contract -> authoritative lifecycle and evidence
```

For a layered request, CB owns only the part it receives: validation, admission,
placement, execution verification and state beneath its ingress. An upstream
gateway remains responsible for its own authentication, routing, retries,
budgets and calls that never enter CB. Imported or reported observations must
not be presented as authoritative CB execution evidence.

| Boundary | Authoritative component |
| --- | --- |
| External workflow triggers, branches and approvals | external orchestrator |
| External gateway-only routing, retries and budgets | external gateway |
| Runtime installation, loading and shutdown | runtime manager, unless explicitly CB-managed |
| External document ACLs, chunking, retrieval, reranking and index lifecycle | external context/vector system |
| Built-in local vector compatibility and tenant-space binding | ContextBridge local RAG store |
| Retained copies outside a CB receipt | external archive |
| Admitted CB job policy and placement | ContextBridge relay |
| CB lease, cancellation and terminal state | ContextBridge relay |
| Verified CB result/artifact evidence | ContextBridge receipt and referenced bytes |
