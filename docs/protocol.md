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

The built-in decision schema only accepts `allow` or `review`. Applications that need a different structured result should add a separate route and schema adapter rather than executing arbitrary model output.
