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

For browser jobs, `session_id` pins follow-ups to one selected conversation. The worker namespaces it by authenticated producer; distinct keys cannot reuse an occupied chat. With no ID, jobs from one producer share its default session. A new session requires an unassigned attached tab unless the extension is set to create a new chat tab or `metadata.contextbridge_new_chat: true` is set on that job. Manual switching requires navigating to the saved conversation URL; a moved tab fails before Send. `browser_profile` selects a provider tab such as `chatgpt` or `gemini`; `model` and `reasoning` are matched against that provider's localized visible menus. These fields are bounded preferences: an unavailable explicit choice is an error, never a silent substitution.

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

The default output limit is 64 KB. A job can request between 256 bytes and 1 MB.

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

A browser result can opt in with `output.artifacts: true`. Up to twelve images, audio/video files, download links, or code blocks from the newly completed turn are accepted. Embedded bytes share a maximum 12 MiB budget and are re-hashed by the local service; protected HTTPS assets may remain authenticated browser references. `output.min_artifacts` requires 1–12 transferred files of any supported type; `output.min_images` requires transferred image files whose decoded bytes match the declared image type; `output.min_media` requires transferred audio/video files with matching file signatures. All requirements accept 1–12 files. References, generated code blocks, and textual claims do not satisfy an image or media requirement. An image-generation prompt does not require selecting a special browser tool; `metadata.contextbridge_image_tool: true` is an optional explicit ChatGPT UI selection. `metadata.contextbridge_music_tool: true` selects Gemini's visible Music tool, with `output.min_media: 1` recommended. This requires explicit access to Gemini's media host on the browser connection and may yield an MP4 player file rather than a standalone audio file.

For a visual input, set `image_base64` and `image_media_type` alongside the prompt. The extension uploads the image through the provider's file input even when that input is visually hidden, then submits the prompt text. Local selector diagnostics are available via `contextbridge browser inspect`; the browser heartbeat reports bounded control attributes and counts only, never prompt values, answer text, files, cookies, or full HTML. These diagnostics remain on the local bridge and are not forwarded to the cluster relay.

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
    "task": "generation",
    "prompt": "Describe the image as structured JSON.",
    "image_base64": "...",
    "output": {"mode": "json"}
  },
  "priority": 20,
  "max_attempts": 3
}
```

Set `requirements.provider` when a job must use a specific ready provider on
the worker, for example `browser` for a taught web-chat tab or `ollama` for a
local Ollama runtime. Workers only advertise providers that are live; a browser
route is eligible only while its extension heartbeat and taught selectors are
ready. Omit the field to let the scheduler choose any live provider for the
requested task.

For E2EE, call `/v1/cluster/assign`, encrypt the payload for the returned node public key, and submit the sealed envelope with the one-time assignment ID and secret. The authenticated additional data is `job:{job_id}:{node_id}`. Results use `result:{job_id}:{node_id}` and a separate derived key.
