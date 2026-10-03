# Models and runtimes

ContextBridge does not require one model vendor. A model becomes usable through
a runtime that can actually execute it; a file name or Hugging Face repository
alone is not an execution capability.

## What can be connected

| Resource | How it joins | What ContextBridge trusts |
| --- | --- | --- |
| Ollama model | Install it in Ollama; CB inventories the running daemon | Capabilities and context length reported by Ollama; missing limits remain unknown |
| Hugging Face GGUF | Declare the repository/file, download it with `contextbridge pull`, and run it through managed llama.cpp | Immutable repository revision plus the file's Hugging Face LFS SHA-256 |
| Existing llama.cpp server | Configure a `llama_cpp` engine with `auto_start: false` | Operator-declared model passport and live health; CB does not own the process |
| OpenAI-compatible runtime/API | Configure an `openai_compatible` engine | Explicit operator-reviewed model, capability, limit, egress and credential settings |
| Other runtime or model family | Put an OpenAI-compatible server or a ContextBridge adapter in front of it | Only the bounded evidence exposed by that configured integration |

This means **many arbitrary Hugging Face GGUF models can be used, but not every
repository on Hugging Face can be executed directly**. The selected llama.cpp
build must support the model architecture and quantization. Raw PyTorch,
Transformers, ONNX and SafeTensors files may be discovered for inventory, but
discovery never pretends that CB has a compatible executor. Gated/private Hugging
Face downloads are not handled by the public downloader today; use an external
runtime or place operator-obtained model bytes behind a reviewed local setup.

## Add a Hugging Face GGUF model

Choose a repository that publishes the actual GGUF as a Hugging Face LFS
object. Add a stable local alias under `models`, then point a llama.cpp engine
and route at the same alias:

```yaml
routes:
  my_model:
    provider: my_model
    model: my_model
    task: generation
    timeout_seconds: 180

engines:
  my_model:
    type: llama_cpp
    model: my_model
    executable: auto
    listen: 127.0.0.1:32148
    auto_start: true
    gpu: prefer
    mode: generation
    timeout_seconds: 180
    # Add only facts you have verified. Omit unknown limits.
    capabilities: [text]
    # context_window_tokens: 32768
    # max_output_tokens: 4096

models:
  my_model:
    repository: owner/repository-GGUF
    file: exact-model-file-Q4_K_M.gguf
    kind: generation
    # Optional but strongest: pin a reviewed immutable HF commit.
    # revision: 0123456789abcdef0123456789abcdef01234567
```

Use a unique loopback port for every managed llama.cpp engine. Then install the
runtime once, download and verify the configured model, and run a bounded test:

```sh
contextbridge runtime install llama.cpp
contextbridge pull my_model
contextbridge models --discover=false
contextbridge doctor
contextbridge cluster chat --provider my_model --model my_model --artifacts off --prompt "Reply exactly with MODEL-OK"
```

`contextbridge pull` resolves an unpinned repository to an immutable commit,
requires LFS SHA-256 metadata for every downloaded file, verifies the bytes,
and persists a bounded installation manifest tied to the alias, repository,
filenames, sizes, resolved revision and digests. `models --discover=false`
shows that identity as `verified_download_manifest`; a conflicting or stale
manifest is ignored rather than overriding operator configuration. A configured
`revision` must already be a 40- or 64-character hexadecimal commit. A mutable
branch name, unverified response, missing file digest or mismatched bytes fails
closed.

Remote pulls use one registry-specific egress policy. Every redirect is
revalidated, limited to five hops, required to remain on HTTPS port 443 under
the official `huggingface.co` or `hf.co` host families, and connected only to a
publicly routable resolved address when no operator-configured proxy is in use.
This keeps storage/CDN redirects working without letting a registry response
turn a model pull into a request to loopback, private, link-local, carrier-grade
NAT, or benchmark networks. Digest verification remains a separate final
integrity check; it cannot replace the outbound network boundary.

The manifest records what CB verified at installation time. Listing it does not
rehash a multi-gigabyte model on every status refresh, so later same-size local
tampering is outside that evidence. Pin `revision` and `sha256` in the reviewed
configuration when an execution or embedding-space policy must rely on that
identity; local filesystem integrity remains an operator boundary.

For a vision GGUF, add the exact `projector_file`, declare `vision` only after
verifying support, and configure the known image limits rather than guessing:

```yaml
engines:
  my_vision:
    type: llama_cpp
    model: my_vision
    executable: auto
    listen: 127.0.0.1:32149
    auto_start: true
    gpu: prefer
    mode: generation
    capabilities: [text, vision]
    max_input_images: 4
    max_image_bytes: 8388608
    max_total_image_bytes: 16777216
    image_media_types: [image/png, image/jpeg, image/webp]

models:
  my_vision:
    repository: owner/vision-model-GGUF
    file: vision-model-Q4_K_M.gguf
    projector_file: mmproj-model-f16.gguf
    kind: generation
```

Those numbers are examples, not inferred truth. CB enforces known hard limits
before execution. An omitted value remains unknown instead of silently becoming
zero or an unlimited promise.

## Let an existing runtime own the model

If Ollama, LocalAI, LiteLLM, vLLM, a Transformers service or another runtime
already owns model download/loading, keep that ownership. Configure its local
or remote OpenAI-compatible endpoint and set the model passport explicitly.
ContextBridge then owns admission, policy, placement, leases, terminal state
and result evidence for the CB job; the external runtime continues to own its
process and model lifecycle.

See [Application integrations](integrations.md#externally-managed-runtimes) and
[Compatibility boundaries](compatibility.md) for the exact authority and
evidence limits.
