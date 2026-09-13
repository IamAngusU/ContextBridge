# Architecture

ContextBridge separates sources, routing, providers, and result validation so each boundary can be reviewed independently.

## Cluster Topologies

One relay protocol supports three useful shapes:

- 1:1: one producer routes to one private worker.
- 1:N: one producer distributes work over a capability group.
- N:N: scoped producer tokens share one pool of grouped workers.

Workers initiate an outbound WebSocket connection to the relay. This avoids inbound ports on private computers. Each heartbeat contains current CPU, RAM generation/speed, GPU utilization/temperature/free VRAM, ready models, task, group, tag, concurrency, queue, and browser-tab slot information. The scheduler filters incompatible nodes first, then ranks compatible nodes by active load, GPU pressure, and available memory.

The relay stores tokens as SHA256 hashes, jobs and history in BoltDB, and queue order in a dedicated priority index. A disconnected normal job is requeued until its retry budget is exhausted. A sealed job is tied to the public key chosen during reservation and cannot move to another node without the producer encrypting a new submission.

One BoltDB relay is a single durable coordination process, not an active-active database cluster. A future HA adapter can implement the same store contract with Postgres and a message broker.

## Components

### Source adapters

Sources submit the common job envelope through authenticated HTTP, stdin, or an inbox folder. The source owns database, website, and SSH credentials. ContextBridge does not need them.

### Local service

The Go service listens on localhost, validates job size and shape, selects a route, records local state, and returns the normalized decision. The dashboard is embedded in the executable and does not load remote JavaScript.

### Runtime and providers

The runtime samples hardware capabilities, inspects Ollama, supervises configured `llama.cpp` processes, and publishes one status snapshot to the CLI and dashboard. Engines bind to localhost. Ordered route fallbacks determine which engine is tried next.

The Ollama provider sends a trusted task wrapper and optional image to a configured local model. The `llama_cpp` provider uses the local OpenAI-compatible chat and embedding endpoints. The browser provider leases work to selected extension tabs. Hosted web-chat inference still runs at that provider; the extension replaces a separate API integration, not the provider's compute. A cluster can route parallel browser jobs across several tabs on one PC and across multiple paired PCs.

GPU policy is explicit. `prefer` attempts full offload and records a visible CPU fallback warning. `require` fails the engine when GPU startup fails. `off` starts on CPU. ContextBridge does not report an engine as GPU-backed merely because a GPU exists.

### Browser worker

The extension holds selected tab IDs, origin grants, visual profiles, provider cooldowns, and private session-to-tab affinity in local extension storage. Every tab is a serial slot, while tabs operate concurrently. It receives leased jobs, applies an explicit model/reasoning choice when requested, writes the trusted prompt, and observes response DOM plus send/stop controls. Image loaders and percentages remain progress; visible rate limits/errors become failures; a stable response with an idle composer becomes the final answer. Navigation reattaches to the in-flight conversation without resubmitting, and a generation stuck at 95% or above receives one controlled reload/recovery attempt.

### Storage

Jobs and results are written under the configured data directory. Folder submissions are renamed to a processing suffix before work starts and receive neighboring result files. In-memory browser leases prevent two active workers from claiming the same job at once.

Job counters, provider and model usage, latency, failures, and decision flags are stored atomically in `metrics.json`. Model artifacts use a separate models directory. The local RAG backend persists tenant-separated documents and vectors behind the `vectorstore.Store` interface.

Cluster state uses a separate BoltDB file with owner-only permissions. Jobs have an owner subject derived from the producer token. Producer list, read, and cancel operations are filtered by that subject. Group scopes are enforced both when a producer submits work and when a worker advertises capabilities.

### Pipeline runner

Pipelines are fixed YAML declarations. A step can reference the original JSON input, the previous output, or a named earlier output. Every rendered step must be valid JSON before it enters the queue. Runtime, retries, step count, and model-requested iterations have hard administrator limits. Model output cannot create a route, executable, or extra step.

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
- Browser result delivery interrupted: retain and retry the completion payload.
- Browser tab reload/navigation during work: reattach to the same conversation and observe the existing in-flight response without submitting the prompt twice.
- Provider rate limit or visible error: record an explicit failed state, cool down that tab, and never treat its error card as model output.
- All providers unavailable: return `review` with `providers_unavailable`.
- Service restart during a folder job: the processing file remains visible for operator recovery.
- Worker disconnect during a normal cluster job: requeue on another compatible node within the retry budget.
- Worker disconnect during an E2EE cluster job: fail the bound job so the producer can reserve a new key and encrypt again.
- Relay restart: queued jobs, pairing state, history, token hashes, metrics, and pipeline runs reopen from BoltDB.

## Extension Builds

Shared extension code lives in `extension/src`. Browser-specific manifests live in `extension/manifests`. `scripts/package-extensions.sh` creates deterministic ready directories for Chromium and Firefox. Release archives contain those ready directories and publish each extension separately as well.
