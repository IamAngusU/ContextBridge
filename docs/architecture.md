# Architecture

ContextBridge separates sources, routing, providers, and result validation so each boundary can be reviewed independently.

## Cluster Topologies

One relay protocol supports four useful shapes:

- 1:1: one producer routes to one private worker.
- N:1: several scoped producers share one worker and its configured slots.
- 1:N: one producer distributes work over a capability group.
- N:N: scoped producer tokens share one pool of grouped workers.

These describe routing relationships, not four deployment products. One relay
can serve all of them at the same time. A browser tab is one serial UI slot;
several tabs or local-model slots on one node and several paired nodes can run
independent jobs concurrently. Producer ownership still separates job reads,
cancellation, and session affinity when producers share that pool.

Workers initiate an outbound WebSocket connection to the relay. This avoids inbound ports on private computers. Each heartbeat contains current CPU, RAM generation/speed, GPU utilization/temperature/free VRAM, ready models, task, group, tag, concurrency, queue, and browser-tab slot information. The scheduler filters incompatible nodes first, then ranks compatible nodes by active load, GPU pressure, and available memory.

The same deterministic scheduler produces a bounded routing decision. A
non-executing preview reports eligible candidates, stable rejection reasons,
and additive weighted score components. When a queued job is assigned, that
point-in-time decision is written atomically with the assignment and remains
available for the lifetime of the retained job. The record excludes prompts,
results, full browser-session inventories, and complete hardware snapshots.
It is evidence for why a route was chosen, not a promise that a later preview
will see identical capacity.

The relay stores tokens as SHA256 hashes, jobs and bounded history in BoltDB, and queue order in a dedicated priority index. A worker error, disconnect, or execution timeout is an ambiguous state because the provider may already have accepted the request. ContextBridge therefore fails that execution closed instead of transparently retrying it; the producer must submit a new job explicitly. A sealed job is additionally tied to the public key chosen during reservation and cannot move to another node without the producer encrypting a new submission.

One BoltDB relay is a single durable coordination process, not an active-active database cluster. A future HA adapter can implement the same store contract with Postgres and a message broker.

## Components

### Source adapters

Sources submit the common job envelope through authenticated HTTP, stdin, an
inbox folder, or the bounded local MCP stdio adapter. The adapter is a child of
the MCP host and proxies only status, submit, and result calls to the same
authenticated loopback API; it is not a second router or authorization system.
The source owns database, website, and SSH credentials. ContextBridge does not
need them.

### Local service

The Go service listens on localhost, validates job size and shape, selects a route, records local state, and returns the normalized decision. The dashboard is embedded in the executable and does not load remote JavaScript.

### Runtime and providers

The runtime samples hardware capabilities, inspects Ollama, supervises configured `llama.cpp` processes, and publishes one status snapshot to the CLI and dashboard. Engines bind to localhost. Ordered route fallbacks determine which engine is tried next.

The Ollama provider sends a trusted task wrapper and optional image to a configured local model. The `llama_cpp` provider uses the local OpenAI-compatible chat and embedding endpoints. The browser provider leases work to selected extension tabs. Each worker publishes a bounded per-tab record of profile, state, visible current selection, model choices, reasoning levels, fresh-chat capability, and—only for clustered sessions with a permanent saved conversation—an opaque producer-scoped selector. Chat text, tab titles, public session names, and conversation URLs are excluded, and public node responses redact the selector. When a browser job fixes both profile and model, one ready waiting tab or exact matching owned session must satisfy both instead of the scheduler combining evidence from different tabs on the same node. Hosted web-chat inference still runs at that provider; the extension replaces a separate API integration, not the provider's compute. A cluster can route parallel browser jobs across several tabs on one PC and across multiple paired PCs.

GPU policy is explicit. `prefer` attempts full offload and records a visible CPU fallback warning. `require` fails the engine when GPU startup fails. `off` starts on CPU. ContextBridge does not report an engine as GPU-backed merely because a GPU exists.

### Hardware reporting and placement

A node heartbeat reports every detected GPU separately. With multiple GPUs,
the scheduler evaluates the best single eligible device: an explicit
`min_free_vram_bytes` passes only if at least one GPU has that much free VRAM;
memory on several devices is not silently added together. When no hard minimum
is set, a VRAM estimate learned from genuinely job-attributed history is a soft
ranking hint. A GPU node with headroom is preferred when other signals are
similar, but a compatible zero-GPU/CPU worker remains eligible.

This is placement, not a GPU allocator. ContextBridge does not reserve VRAM,
pin a job to a numbered GPU, infer one slot per GPU, or promise that Ollama or
`llama.cpp` will split a model across devices in a particular way. The local
runtime and its configuration decide actual device use; `max_concurrent`
remains the node's admission limit. A rack is represented by its paired worker
nodes. A multi-GPU host can report all of its devices as one node, but should
not be multiplied into independent worker processes merely to manufacture
slots: separate processes currently lack a shared machine-wide RAM/VRAM
semaphore.

### Browser worker

The extension holds explicitly attached tab IDs, origin grants, visual profiles, provider cooldowns, and private session-to-tab/URL affinity in local extension storage. Optional fresh-tab discovery verifies the URL and an empty composer/conversation twice before attachment; existing or uncertain chats fail closed. Detaching removes a tab from future leases, though work already submitted to a website cannot be unsent. Every tab is a serial slot, while tabs operate concurrently. A completed browser job reports its actual execution tab to correct relay affinity. If a saved conversation is reopened elsewhere, bounded opaque telemetry lets the relay choose the candidate while the exact URL proof remains local. It receives leased jobs, applies an explicit model/reasoning choice when requested, writes the trusted prompt, and observes response DOM plus send/stop controls. Gemini's mode choices are read from its live picker rather than a fixed catalog. Image loaders and percentages remain progress; visible rate limits/errors become failures; a stable response with an idle composer becomes the final answer. Navigation reattaches to the in-flight conversation without resubmitting, and a generation stuck at 95% or above receives one controlled reload/recovery attempt.

### Storage

Jobs and results are written under the configured data directory. Folder submissions are renamed to a processing suffix before work starts and receive neighboring result files. In-memory browser leases prevent two active workers from claiming the same job at once.

Job counters, provider and model usage, latency, failures, and decision flags are stored atomically in `metrics.json`. Model artifacts use a separate models directory. The local RAG backend persists tenant-separated documents and vectors behind the `vectorstore.Store` interface.

Hardware and job usage have deliberately different scopes. CPU load, free RAM,
GPU utilization, temperature, and free VRAM in a node heartbeat are
**system-wide snapshots**; browsers, model runtimes, and unrelated processes
can all contribute to them. ContextBridge never labels a change in those
snapshots as “this job used X MiB.” Per-job token counts, queue time, and
elapsed compute time are recorded separately. Peak RAM/VRAM/GPU values are
accepted and aggregated only when an execution engine explicitly marks them
with `resource_scope: "job"`; missing attribution remains unknown instead of
being guessed. The current console does not expose ContextBridge process RSS or
reserve a requested amount of RAM per job.

Cluster state uses a separate BoltDB file with owner-only permissions. Jobs have an owner subject derived from the producer token. Producer list, read, and cancel operations are filtered by that subject. Group scopes are enforced both when a producer submits work and when a worker advertises capabilities.

Relay retention runs once during startup and periodically while the dispatcher is active. In one Bolt write transaction it removes only exact terminal job and pipeline-run states plus old/excess event rows and old/excess opaque session placements. A terminal job's payload, result or sealed envelopes, time index, and any defensive queue entry are removed together; active and unrecognized states fail safe and remain. The placement cache contains only a hashed owner/session/scope key, node ID, optional browser tab ID, and update time; it has its own configured count and shares the configured age. The newest configured count is retained only while it is also within age, so age and count are independent upper bounds. Before job deletion, summary fields already exposed by the lifetime overview are added to a separate cumulative record. Node compute/cost counters remain sourced from cumulative node records and are not rewritten. Freed Bolt pages are reusable; retention bounds continued detail growth after the database high-water mark but is not an online file-compaction operation.

### Pipeline runner

Pipelines are fixed YAML declarations. A step can reference the original JSON input, the previous output, or a named earlier output. Every rendered step must be valid JSON and fit the relay payload limit before it enters the queue. Runtime, step count, and model-requested iterations have hard administrator limits. A declared attempt budget never overrides the at-most-once rule after ambiguous execution; model output cannot create a route, executable, or extra step.

### Model registry

Model manifests identify a Hugging Face repository, exact GGUF filename, optional projector, task, and embedding prefixes. Downloads use partial files, resume when supported, verify LFS SHA256 metadata, and become visible only after an atomic rename. Read-only discovery also inventories reachable Ollama runtimes and user-selected GGUF, ONNX, or SafeTensors paths, inferring likely capabilities, parameters, quantization, and memory demand without moving or loading files.

The managed `llama.cpp` installer selects an official release asset for the operating system and detected backend. It verifies the SHA256 digest supplied by the GitHub release API and rejects archive path traversal before extraction.

### RAG extension point

`rag_ingest` embeds passages and upserts them into the configured vector store. `rag_query` embeds a query and returns ranked matches. The included local backend is suitable for private and modest collections. Larger deployments can implement the same store interface with a dedicated vector database without changing the public job envelope.

## Data Flow

1. A source creates a job with trusted instructions and untrusted content in separate fields.
2. ContextBridge validates the envelope and chooses a route.
3. The primary provider receives a prompt that marks submitted content as data.
4. Provider output is parsed into the requested decision, JSON, text, embedding, or RAG schema.
5. Invalid moderation output becomes `review`; invalid generic output is returned as an explicit error.
6. The normalized result returns to the source and is stored locally.

## Failure Behavior

- Provider unavailable: try the next configured fallback.
- Browser profile mismatch: return a browser automation error as `review`.
- Browser result delivery interrupted: retain and retry the already produced
  completion payload; do not execute the provider request again.
- Browser tab reload/navigation during work: reattach to the same conversation and observe the existing in-flight response without submitting the prompt twice.
- Provider rate limit or visible error: record an explicit failed state, cool down that tab, and never treat its error card as model output.
- All providers unavailable: return `review` with `providers_unavailable`.
- Service restart during a folder job: the processing file remains visible for operator recovery.
- Worker error, disconnect, or execution timeout: fail the job because provider-side execution may already have happened; require an explicit new submission instead of an ambiguous automatic retry. This is an at-most-once execution policy, not an exactly-once guarantee—the provider may have completed work whose final result could not be observed.
- E2EE resubmission after such a failure: reserve a new worker key and encrypt again.
- Relay restart: queued jobs, pairing state, retained history, token hashes, metrics, and active/recent pipeline runs reopen from BoltDB; startup retention applies the configured detail bounds.

## Extension Builds

Shared extension code lives in `extension/src`. Browser-specific manifests live in `extension/manifests`. `scripts/package-extensions.sh` creates deterministic ready directories for Chromium and Firefox. Release archives contain those ready directories and publish each extension separately as well.
