# Job protocol

ContextBridge accepts jobs at `POST /v1/jobs` and from JSON files placed in the configured inbox directory. HTTP requests require `Authorization: Bearer <token>`.

```json
{
  "source": "my-app",
  "route": "default",
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

The synchronous HTTP response contains the normalized decision. Folder jobs are renamed while processing and produce a neighboring `.result.json` file.

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
| `POST` | `/jobs/{id}/complete` | Return a normalized decision |
| `POST` | `/heartbeat` | Publish non-sensitive tab and worker status |
| `GET` | `/profiles` | Read configured YAML browser profiles |

The extension stores an undelivered completion locally and retries it. It does not resubmit the same prompt just because the completion response was interrupted.

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
