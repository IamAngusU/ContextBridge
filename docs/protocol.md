# Job protocol

ContextBridge accepts jobs at `POST /v1/jobs` and from JSON files placed in the configured inbox directory. HTTP requests require `Authorization: Bearer <token>`.

```json
{
  "source": "my-app",
  "route": "default",
  "provider": "browser",
  "session_id": "campaign-42",
  "browser_profile": "chatgpt",
  "model": "gpt-6-astra",
  "reasoning": "high",
  "kind": "moderation",
  "prompt": "Trusted instructions written by the operator",
  "text": "Untrusted content supplied by a user",
  "image_base64": "optional-base64",
  "image_media_type": "image/webp",
  "metadata": {
    "record_id": "123"
  },
  "output": {
    "mode": "decision"
  }
}
```

`provider` is optional and must name the primary provider or one of the
configured fallbacks on the selected route. It is useful when a caller must use
the taught web-chat tab even while a local model is also online. Cluster workers
automatically enforce `requirements.provider` on the forwarded local job.

The synchronous HTTP response contains the normalized decision. Folder jobs are renamed while processing and produce a neighboring `.result.json` file.

## Trusted prompt and submitted data

The Core always builds the safety and output-contract wrapper. It is not an
extension setting: browser teaching changes selectors, never this trust
boundary. Operators edit the job's `prompt`, while external or end-user input
belongs in `text`. Putting untrusted material directly in `prompt`
intentionally treats it as trusted instructions and should be avoided.

The generated wrapper supports exact JSON requests. Use
`output.mode: "json"`, declare essential top-level `required_keys`, and
state types, allowed values, and exact semantics in `prompt`. Required keys
are only a shallow presence check, not full JSON Schema validation; callers
must validate types, ranges, unknown fields, and any value used for a
consequential action. Visual profile YAML cannot replace or disable the
wrapper. See
[Browser sessions and prompt contracts](browser-sessions-and-prompts.de-en.md).

For browser jobs, `session_id` pins follow-ups to one selected conversation. The worker namespaces it by authenticated producer; distinct keys cannot reuse an occupied chat. With no ID, jobs from one producer share its default session. A new session requires an unassigned attached tab unless the extension is set to create a new chat tab or `metadata.contextbridge_new_chat: true` is set on that job. Cluster producers must additionally set `requirements.browser_fresh_chat: true`, because the relay cannot inspect an E2EE payload to discover that metadata; `cluster chat --new-chat` does both. The extension can also be set to open a fresh chat for every job, or an individual job can request this with `metadata.contextbridge_new_chat_per_job: true` plus authenticated `requirements.browser_ephemeral_chat: true`; `cluster chat --new-chat-per-job` sets the complete pair. Ephemeral requires fresh. Per-job mode intentionally prevents conversation follow-ups; use per-session mode for multi-step exchanges. Completed, ContextBridge-created per-job tabs may be closed after a two-minute idle grace period if the user enables the opt-in popup switch. `metadata.contextbridge_close_tab_after_job: true` requests the same guarded cleanup for one explicitly auto-created job tab. Manual switching requires navigating to the saved conversation URL; a moved tab fails before Send. `browser_profile` selects a provider tab such as `chatgpt` or `gemini`; `model` and `reasoning` are matched against that provider's localized visible menus. These fields are bounded preferences: an unavailable explicit choice is an error, never a silent substitution.

```json
{
  "verdict": "allow",
  "flags": [],
  "confidence": 0.97,
  "model": "gemma3:4b",
  "provider": "ollama",
  "latency_ms": 842
}
```

## Output Modes

`decision` is the default and remains compatible with InkWall. Its vocabulary accepts only `allow` or `review`.

`json` returns a parsed JSON value. Use `required_keys` when the caller needs a minimum top-level object contract:

```json
{
  "output": {
    "mode": "json",
    "required_keys": ["language", "topic", "summary"],
    "max_bytes": 8192
  }
}
```

`text` returns bounded plain text. ContextBridge never evaluates text or JSON as code. A calling application must still validate values for its own domain before using them.

`embedding` returns native vectors rather than JSON encoded inside a string:

```json
{
  "mode": "embedding",
  "embeddings": [[0.12, -0.04, 0.88]],
  "dimensions": 3,
  "tenant_id": "acme",
  "provider": "llama_cpp",
  "model": "jina-v4-retrieval"
}
```

An embedding job accepts `text` or up to 256 values in `texts`. Set `metadata.embedding_role` to `query` or `passage` when the model manifest defines role prefixes.

`rag_ingest` accepts up to 256 documents with an ID, text, and optional metadata. `rag_query` accepts `query`, optional `tenant_id`, and `top_k` from 1 to 50. Tenant IDs are used as hard partitions by the local store.

The default output limit is 64 KiB. A job can request between 256 bytes and 1 MiB. Limits are measured on UTF-8 bytes rather than displayed characters, so non-ASCII text may use more than one byte per character. Text at the exact limit is preserved. If a longer text answer is bounded, the returned output includes `"truncated": true`; `cluster chat` requests the 1 MiB maximum and prints a warning instead of silently presenting that prefix as a complete answer. Structured JSON is never returned as a truncated prefix: an over-limit JSON result fails its output contract.

## Browser Worker Endpoints

The extension uses authenticated endpoints under `/v1/browser`:

| Method | Endpoint | Purpose |
| --- | --- | --- |
| `GET` | `/jobs/next` | Lease the next matching browser job |
| `POST` | `/jobs/{id}/lease` | Renew an active browser lease |
| `GET` | `/jobs/{id}/progress` | Read the latest progressive answer snapshot |
| `POST` | `/jobs/{id}/progress` | Publish a progressive answer snapshot |
| `POST` | `/jobs/{id}/complete` | Return a normalized decision |
| `POST` | `/heartbeat` | Publish non-sensitive tab and worker status |
| `POST` | `/drafts` | Save an opted-in unsent editor draft locally before clearing it |
| `GET` | `/profiles` | Read configured YAML browser profiles |

The extension stores an undelivered completion locally and retries it. It does not resubmit the same prompt just because the completion response was interrupted.

Draft preservation is off by default and can be enabled under **Manage other tabs**. Before a job starts, the extension saves a non-empty text draft through the authenticated local `/v1/browser/drafts` endpoint and clears the editor only if the text still matches. A failed save, changed draft, or editor that refuses clearing aborts the job without sending its prompt. The server stores JSONL with timestamp, provider, tab ID/title, origin (not the full chat URL), session ID, and draft text in `~/.contextbridge/draft-history.jsonl`; it retains at most 1 MiB, dropping oldest records. The file is local plaintext, not part of E2EE or the relay. Do not enable this on a shared operating-system account if those drafts are sensitive.

Unsealed cluster jobs expose the latest browser snapshot in the job's `progress`
field. `sequence` is monotonic, `text` contains at most 1 MB of UTF-8, and
`phase` is `submitting`, `generating`, `stabilizing`, `recovering`, `rate_limited`, or `final`. `detail` carries a short status and `percent` carries a determinate 0–100 value when the page exposes one, such as ChatGPT image generation. These fields are telemetry and are never normalized as answer text. Plaintext progress is disabled for sealed jobs.

A browser result can opt in with `output.artifacts: true`. Up to twelve images, audio/video files, download links, or code blocks from the newly completed turn are accepted. Embedded decoded bytes share a maximum 12 MiB budget; the exact limit is accepted and one byte beyond it is rejected. The local service recomputes size and SHA-256 rather than trusting browser metadata. Protected HTTPS assets may remain authenticated browser references. `output.min_artifacts` requires 1–12 transferred files of any supported type; `output.min_images` requires transferred image files whose decoded bytes match the declared image type; `output.min_media` requires transferred audio/video files with matching file signatures. All requirements accept 1–12 files. References, generated code blocks, and textual claims do not satisfy an image or media requirement. An image-generation prompt does not require selecting a special browser tool; `metadata.contextbridge_image_tool: true` is an optional explicit ChatGPT UI selection. `metadata.contextbridge_music_tool: true` selects Gemini's visible Music tool, with `output.min_media: 1` recommended. This requires explicit access to Gemini's media host on the browser connection and may yield an MP4 player file rather than a standalone audio file.

Scheduled workflows may pass the immediately preceding step's embedded image
bytes into a browser, Ollama, or llama.cpp follow-up. ContextBridge decodes the
bytes, verifies their signature, recomputes decoded size and SHA-256, binds the
handoff to the schedule/session and source job in metadata, and enforces the
8 MiB image-input bound. It never fetches a URL-only artifact for this purpose.
Local-model handoff is image-only and fails closed when image input is disabled;
general files remain browser-only.

For a visual input, set `image_base64` and `image_media_type` alongside the prompt. One decoded image may contain up to exactly 8 MiB; empty inputs and inputs one byte over the limit are rejected before dispatch. The extension uploads the image through the provider's file input even when that input is visually hidden, then submits the prompt text. Local selector diagnostics are available via `contextbridge browser inspect`; the browser heartbeat reports bounded control attributes and counts only, never prompt values, answer text, files, cookies, or full HTML. These diagnostics remain on the local bridge and are not forwarded to the cluster relay.

## Tunnel Status Endpoint

An external tunnel supervisor can publish authenticated state at `POST /v1/tunnel/heartbeat`. ContextBridge marks the tunnel stale when heartbeats stop. This endpoint reports transport health only; it does not create a tunnel or accept credentials.

```json
{
  "state": "connected",
  "target": "review@example.com",
  "transport": "SSH with encrypted payloads",
  "local_port": 8788,
  "remote_port": 8788
}
```

## Limits

- prompt: 20,000 bytes
- text: 200,000 bytes
- decoded image: 8 MB
- JSON request body: 12 MB
- embedding batch: 256 inputs
- embedding dimensions: 32,768 maximum
- RAG query results: 50 maximum

Unknown JSON fields are rejected. HTTP responses use `Cache-Control: no-store`.

Client-supplied job IDs may contain 1 to 128 ASCII letters, numbers, dots, underscores, and hyphens. Path separators and repeated dots are rejected. A duplicate ID returns HTTP `409` and never replaces an earlier job.

## Cluster Protocol

Cluster endpoints use separate admin, observer, producer, and node bearer credentials. Pairing request and polling endpoints use short-lived device credentials and rate limits.

| Method | Endpoint | Role | Purpose |
| --- | --- | --- | --- |
| `POST` | `/v1/pair/request` | Public, rate limited | Begin worker pairing |
| `POST` | `/v1/pair/token` | Device code | Poll once for a node credential |
| `GET` | `/v1/pairings` | Admin | List pending pairing requests |
| `POST` | `/v1/pairings/{code}/approve` | Admin | Approve one worker |
| `GET` | `/v1/cluster/overview` | Admin, observer, producer | Read aggregate status |
| `GET` | `/v1/cluster/nodes` | Admin, observer, producer | Read node capabilities and load |
| `POST` | `/v1/cluster/jobs` | Admin, producer | Queue a normal or sealed job |
| `GET` | `/v1/cluster/jobs/{id}` | Scoped role | Read one allowed job |
| `DELETE` | `/v1/cluster/jobs/{id}` | Admin, producer | Cancel one allowed job |
| `POST` | `/v1/cluster/assign` | Admin, producer | Reserve an E2EE worker key |
| `GET` | `/v1/cluster/workers/connect` | Node | Upgrade to the worker WebSocket |
| `POST` | `/v1/cluster/pipelines/{name}/run` | Admin, producer | Start a declared pipeline |

Detailed terminal records and opaque session placements are subject to relay retention. By default a startup and five-minute periodic sweep keeps only terminal jobs from the last 30 days (at most 500), events from the last 30 days (at most 5,000), terminal pipeline runs from the last 30 days (at most 200), and pseudonymous session placements from the last 30 days (at most 5,000). Age and count are both upper bounds. Active or unrecognized lifecycle states are never swept. Once detail is pruned, its job or pipeline-run endpoint returns not found and its prompt, result, sealed envelopes, and per-record ownership metadata are no longer available; an expired/excess placement loses its cached node/tab affinity but may still be rediscovered from live opaque browser evidence. Aggregate lifetime counts remain available from `/v1/cluster/overview`. Configure the guarded limits with `cluster.relay.retention_days`, `max_terminal_jobs`, `max_events`, `max_terminal_pipeline_runs`, `max_session_placements`, and `retention_sweep_seconds`.

A cluster job contains routing metadata and one local ContextBridge job as its payload:

```json
{
  "requirements": {
    "task": "vision",
    "provider": "browser",
    "group": "media",
    "required_tags": ["private"],
    "vision": true,
    "min_free_vram_bytes": 8589934592
  },
  "payload": {
    "route": "default",
    "task": "vision",
    "prompt": "Describe the image as structured JSON.",
    "image_base64": "...",
    "output": {"mode": "json"}
  },
  "priority": 20
}
```

Set `requirements.provider` when a job must use a specific ready provider on
the worker, for example `browser` for a taught web-chat tab or `ollama` for a
local Ollama runtime. Workers only advertise providers that are live; a browser
route is eligible only while its extension heartbeat and taught selectors are
ready. Omit the field to let the scheduler choose any live provider for the
requested task.

Capability frames may also include `automatic_tasks_by_provider`. This map is
the authoritative set of model-less tasks the worker can route automatically
for each provider; an empty provider entry deliberately means "no automatic
task". A missing field identifies an older worker and keeps the legacy
capability interpretation. Concrete entries in `models` remain authoritative:
an incompatible or missing fixed model is never upgraded by a generic fallback,
and all requested properties (for example `task: vision`) must be satisfied by
the same model on the selected provider.

Browser workers additionally report bounded `browser_sessions`. A session may
include its provider profile, waiting/working state, visible current model and
reasoning, capped model/reasoning choice labels, whether that origin can create
a fresh chat, and an optional producer-scoped opaque session selector. It never
includes chat content, a tab title, a conversation URL, or a public session
name. The selector is accepted only in the fixed `cb:` plus SHA-256 form and is
removed from `/v1/cluster/nodes` responses; it is routing evidence internal to
the relay. New schedulers require one ready waiting session or the exact
matching owned session to jointly satisfy an explicit browser profile and
model; a node-wide model union cannot make a Gemini slot satisfy a ChatGPT
request. Current-model-only telemetry and older workers without per-session
choices retain a conservative compatibility path.

The relay stores the concrete tab reported by the worker after execution, not
merely the tab it predicted before the lease. On a later turn, a matching live
opaque selector outranks stale numeric tab placement on that same worker. The
extension still rechecks its private exact saved URL immediately before Send;
duplicate selector claims fail closed. If no live selector exists, recovery may
use an unpinned waiting tab on the same worker so the extension can prove the
saved URL locally. A fresh-chat launcher is allowed only for ChatGPT/Gemini,
with matching host permission and fewer than 16 attached tabs.

For a provider-specific browser job, also set
`requirements.browser_profile` to `chatgpt`, `gemini`, or the exact learned
profile name and repeat the value in the local job payload's
`browser_profile`. The requirement is a hard scheduler filter: a node without
an attached ready waiting tab or exact matching owned session of that profile
is not eligible. `cluster chat
--profile` and `cluster selftest` set both fields automatically. A browser
profile is rejected with any provider other than `browser`.

The worker binds the relay-approved `requirements.task` to local execution;
the effective local route task must match it. A worker error, disconnect, or
execution timeout is terminal because the provider may already have accepted
the request. `max_attempts` remains a bounded compatibility field in the wire
schema, but it does not authorize automatic replay after an ambiguous
assignment. Explicit resubmission creates a new execution. Retrying delivery
of an already produced completion payload is allowed because it does not call
the provider again.

For E2EE, call `/v1/cluster/assign` with an object containing `requirements`
and the optional `tenant_id`, encrypt the payload for the returned node public
key, and submit the sealed envelope with the one-time assignment ID and secret.
The assignment fixes the authenticated producer subject, tenant, complete
requirements (including `session_id`), selected node, job ID, and first
assignment attempt. A producer must reject a response that changes an
explicitly requested value before encrypting; the only server-filled routing
value is a blank group when the producer token authorizes exactly one group.
An exact browser-tab reservation is therefore intentionally not relocatable
after sealing. If the tab or worker changes, the producer must make a new
reservation and encrypt a new submission rather than silently retargeting the
existing ciphertext.

Worker WebSocket protocol version 2 carries the assignment `attempt` on
`started`, `progress`, and `result` frames. The relay changes job state only
when both the authenticated node ID and attempt match the current assignment,
so a foreign worker or a stale execution generation cannot complete it.

`cluster.JobAAD` and `cluster.ResultAAD` serialize a compact JSON envelope with
scheme `contextbridge.cluster.e2ee.v2`, purpose `job` or `result`, and a context
containing `job_id`, `node_id`, `attempt`, `owner_subject`, optional `tenant_id`,
and the complete `requirements` object. The worker reconstructs this context
from the received outer job before decrypting; any changed execution or
namespace field fails AES-GCM authentication before local execution. Results
authenticate the same context under the separately derived response key.
