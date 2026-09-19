# Product directions, not release promises

This document records design candidates. **Nothing on this page is a shipped
capability, release commitment, compatibility guarantee, or delivery date.**
Current behavior is documented in the [README](../README.md),
[protocol](protocol.md), and [architecture](architecture.md). A candidate moves
out of this file only after its security boundary, limits, migrations,
documentation, and tests ship together.

Status words used here:

- **Planned direction** means the design fits ContextBridge and is worth
  specifying next, but has no promised version or date.
- **Exploratory** means the idea still needs threat modeling, user evidence,
  and a decision to build it.

## Compatibility and adoption

### OpenAI-compatible ingress — shipped alpha, broader surface remains planned

The authenticated, bounded `models` and `chat/completions` subset ships in
0.5.75 under `/openai/v1`. It translates requests into ordinary ContextBridge
jobs and offers non-streaming output plus final-result SSE. See the exact
[surface and limits](openai-compatible-api.md). Native jobs remain the
authoritative, more expressive interface for routing, E2EE, artifacts, and
failure states.

`/embeddings`, `/responses`, provider-token streaming, tool calls, cancellation,
artifact-return conventions, and a shared OpenAPI/JSON Schema remain planned
work. They will ship only where their failure and ownership semantics can be
mapped without pretending the compatibility protocol is richer than it is.

### Small application clients — PHP baseline shipped, broader SDKs planned

Producer-scoped idempotent cluster admission is now part of the native
protocol: an exact retry returns the retained job, changed content conflicts,
and the binding is durable and retention-aware. It remains submission
deduplication—not a claim of universal exactly-once provider side effects.
See the [protocol](protocol.md) for the shipped contract.

A deliberately small, dependency-free PHP 8.1+ client now ships under
[`examples/php`](../examples/php). It submits with a required idempotency key,
reads and waits with bounded safe-read backoff, cancels once, and verifies and
saves embedded artifacts without following URL references. It enforces HTTPS
off loopback and is intended for a backend or shared-hosting process—never for
shipping producer credentials to browser JavaScript or a mobile binary. See
the [PHP guide](php-client.md). Broader generated SDKs remain exploratory until
the native and compatibility schemas converge.

### Remote MCP gateway and MCP client — exploratory

The shipped [MCP adapter](mcp.md) is deliberately smaller: one local stdio
server exposes bounded status, submit, and result tools through the existing
authenticated local API. It is not a remote gateway and not an MCP client.

A future direction may investigate Streamable HTTP with real producer scopes,
or ContextBridge as an MCP client that registers remote tools as worker
capabilities. The useful boundary would be a private tool on an outbound-
connected worker without making that workstation publicly reachable.

Every remote tool, resource, and prompt would need an explicit producer scope,
bounded input/output, provenance, and an attributable worker/run. MCP output
would remain untrusted input to subsequent model steps. No tool response may
create a route, credential, permission, or recursive delegation on its own.

## Routing and operations

### Adaptive routing and circuit breakers — planned direction

Build on the scheduler's current node load, free RAM/VRAM, loaded-model state,
browser slots, groups, and tags. Candidate rolling observations include success
rate, queue time, P50/P95/P99 latency, time to first token, throughput, cold
start time, rate-limit frequency, classified failures, and operator-supplied
cost estimates. Policies could express preferences such as `prefer_loaded`,
`max_latency_ms`, `local_only`, `privacy=e2ee`, or `prefer_node`.

A circuit breaker may exclude a repeatedly failing route **before assignment**.
It must never turn an ambiguous post-submit browser or model failure into a
transparent retry; the existing at-most-once execution boundary still wins.
The shipped route-explanation baseline already reports bounded deterministic
evidence, stable rejection reasons, and weighted score components. This future
direction may add rolling observations only after their provenance, sample
size, decay, and failure classification are trustworthy; it must preserve the
same content-minimizing decision record and first run in shadow mode.

### Privacy-default OpenTelemetry and policy — planned direction

Add optional OpenTelemetry traces and metrics around queueing, selection,
worker execution, local inference, browser upload, artifact verification,
recovery, and explicit fallback. Prompt text, response text, files, URLs,
browser titles, and user-entered node names must remain **off by default**;
content export would require a separate informed opt-in. Trace and tenant IDs
also need bounded cardinality, retention guidance, and authorization.

The same policy layer could enforce per-producer quotas, concurrency, allowed
models/nodes, and operator-defined budgets with auditable allow/deny reasons.
External collectors become part of the deployer's data flow and are not a
privacy-free default. See [privacy and compliance readiness](compliance-readiness.md).

### Runtime-aware local scheduling — exploratory

Prefer already loaded compatible models, measure cold starts, pre-warm models
for known scheduled work, batch compatible embeddings, and evict idle models
under operator-defined VRAM pressure. Pre-warming must be opt-in and respect
energy, temperature, memory, time-window, and cost budgets. Telemetry is a
routing hint, not a GPU reservation or proof that a model will fit.

### Worker drain and provider-scoped pause — planned direction

Let an operator stop new assignments while existing jobs finish, or pause only
local GPU execution while attached browser work remains eligible (and vice
versa). State must be visible to route explanation, survive a worker restart
when configured as administrative policy, and never cancel ambiguous work in
flight. Loaded compatible models can remain a soft preference; drain/pause,
capability checks, producer policy, memory limits, and exact session ownership
stay hard constraints.

### Shared multi-relay resource budget — planned direction

Replace independently configured worker-process slot limits with one local
admission controller. Allocate browser tabs, RAM, GPU memory, CPU, and loaded-
model capacity across relays under owner-defined quotas. Reserve estimated
resources before accepting work and release them after completion,
cancellation, or crash. A measured VRAM estimate is not a guaranteed safe
limit, and multi-GPU memory must not be summed unless a runtime explicitly
supports and reports that allocation.

## Data and workflow planes

### Resumable artifact plane — planned direction

Move large file bytes out of the JSON control envelope. A job could reference
an artifact ID, media type, exact size, SHA-256, and optional E2EE metadata,
while a separate bounded channel transfers authenticated chunks with resume
and integrity verification. Content addressing may reduce repeated transfers,
but cross-tenant deduplication can leak content equality and therefore must not
be a default. Encrypted chunks, partial-file cleanup, quotas, cancellation, and
malicious compression/media inputs need dedicated tests before limits change.

### Durable runs and approval gates — planned direction

Evolve fixed pipelines into versioned runs with checkpoints, idempotency keys,
per-step retry policy, explicit fallback, resume, and human approve/edit/reject
gates. A checkpoint proves orchestration state; it does not prove that an
external provider never accepted a request. Any step with an ambiguous
post-submit outcome remains non-replayable until an operator makes an explicit
decision. A general visual agent-builder or unrestricted graph language is not
required for this direction.

### Optional intent planner producer — exploratory

An intent planner may eventually translate a high-level request into a
versioned, bounded run specification, but it would live **above** the relay as
an ordinary untrusted producer. A deterministic validator would enforce task,
provider, egress, cost, step, parallelism, runtime, and artifact limits before
ContextBridge accepts any generated plan. The core must remain fully useful
without an LLM planner, and a plan may never create credentials, widen policy,
disable E2EE, register arbitrary tools, or infer that an ambiguous post-submit
failure is safe to retry.

The useful contract is: AI may propose work; ContextBridge authorizes,
schedules, attributes, and verifies it. Replanning can act only on explicit
failure classes such as `safe_to_reassign`; unknown completion continues to
require an operator decision.

### Large-document reference application — exploratory

A separate reference application may demonstrate one durable run over hundreds
of PDFs using artifact references, bounded dynamic work units, a shared queue,
typed evidence, verification steps, and final synthesis. It should not divide
work by raw document count or push every document into every model context.
Faster compatible workers naturally claim more units, while provenance-rich
claims and contradiction candidates remain tenant- and run-scoped.

PDF parsing, chunking, fact schemas, semantic normalization, contradiction
rules, and report structure belong to that application—not the relay or
scheduler. Model-produced annotations remain candidates with provenance,
version, review state, and optional expiry; they never become global policy or
silent prompt instructions for later jobs.

### Opt-in multi-agent conversations — exploratory

Let an operator define a bounded workflow in which ChatGPT, Gemini, and local
models can propose subtasks, hand verified text or artifacts to one another,
and request a second opinion. This is not an unrestricted model-to-model chat
room. The operator chooses participants, maximum turns, budget, artifact types,
and final authority; every hop remains an ordinary attributable ContextBridge
job. Model output is untrusted input to the next hop, recursive delegation is
capped, sessions stay producer-scoped, and a model can never grant itself a
tab, tool, credential, or route. A readable transcript should show who
requested, executed, and approved each step.

## Browser reliability

### Passive model discovery — planned direction

Observe model and reasoning choices only when the user or provider naturally
opens a semantic menu, then re-scan after meaningful menu mutation, account
change, or new conversation with a bounded idle fallback. Validate a discovered
choice against the active conversation and retain provider, locale, and adapter
version as evidence. A public model catalog is not proof that one account can
select a mode. Discovery must not send a prompt, reload a page, dismiss a user
menu, or disturb a draft.

### Provider adapter state machines and drift quarantine — planned direction

Define a small adapter contract for capability detection, prompt submission,
progress, final-state proof, upload, artifact extraction, rate limits, and
recovery. Bounded DOM/accessibility fingerprints can detect meaningful drift.
When confidence falls below a reviewed threshold, quarantine the tab and show
the evidence instead of clicking a plausible but unverified element. Visual
teaching remains a local fallback. A signed community registry is exploratory
and would require publisher identity, version pinning, review, rollback, and a
strict ban on remotely hosted executable code.

### Cross-browser session registry — exploratory

Give every extension installation a persistent random instance ID. The local
bridge could aggregate per-instance tab heartbeats instead of overwriting one
browser status. A tab ID is not globally unique, so leases and affinity would
use `(instance ID, tab ID)`. An authenticated, instance-targeted detach request
must reach only the owning extension. No registry update should broadcast
prompt text, full DOM, cookies, or answer content.

### Provider-state fixtures — planned direction

Collect redacted, minimal fixtures for safety-check pauses, capacity pauses,
temporary tool modes, disabled send controls, stale Stop controls, and rate
limits. Each fixture should contain only the roles/labels/state required to
reproduce classification. Prefer state recovery over unconditional reloads,
and never turn an error card into answer text.

## Later infrastructure candidates

Active-active relay high availability is exploratory. The current single
durable BoltDB relay would need a shared transactional store, message/lease
coordination, connection ownership, migrations, disaster recovery, and a
tested consistency model. Copying a BoltDB file or running two uncoordinated
relays is not high availability.

## Deliberate non-goals for now

- a general autonomous browser agent
- a visual workflow clone of existing automation products
- a ContextBridge-trained foundation model
- Kubernetes as a requirement for small private pools
- silent provider retries after an ambiguous send
- content-rich telemetry enabled by default

The intended differentiator stays narrow: user-owned execution policy over
explicit browser and local-compute capabilities, with observable routing,
verified artifacts, bounded data, and honest failure states.
