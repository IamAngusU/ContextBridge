# Application integrations

ContextBridge exposes several bounded inputs over the same routing and result
normalization core. They do not create separate trust rules.

## Native JSON jobs

Use the authenticated local or relay API when the caller can speak Job Contract
v1 directly. Validate without dispatching first:

```sh
contextbridge cluster contract validate \
  --config ./config.yml \
  --file ./examples/cluster-job.json
```

Then explain or submit:

```sh
contextbridge route explain --config ./config.yml --file ./examples/cluster-job.json
contextbridge cluster submit --config ./config.yml --file ./examples/cluster-job.json
```

The reusable schema is [Job Contract v1](schemas/job-contract-v1.schema.json).
Relay admission and worker execution both revalidate the bounded contract.
For a minimal Ollama request without group/tag prerequisites, start with
[the text and JSON pool examples](../examples/pool/README.md). That guide also
separates per-job choices, operator authority and supported file inputs.

## OpenAI-compatible input

Generate the exact settings for the current installation without printing its
secret:

```sh
contextbridge integrate openai
```

For an application that reads OpenAI-style environment variables, create a
new private file in one step:

```sh
contextbridge integrate openai --write-env .contextbridge.env
```

The command refuses to overwrite an existing file. It writes
`OPENAI_BASE_URL`, `OPENAI_API_KEY` and `OPENAI_MODEL`; the file is excluded by
the repository's default `.gitignore`. File mode `0600` is requested where the
platform supports Unix permissions. Keep the file private and load it only
into the intended local application. Use `--show-token` only for an explicit
copy operation; normal terminal and JSON output remain redacted.

Verify the local service, credential and configured route without sending an
inference request:

```sh
contextbridge integrate openai --check
```

An actual model call is separate and explicit because it may consume local
compute, network egress or paid API credit:

```sh
contextbridge integrate openai --live
```

The live check uses one bounded exact-reply prompt and fails when the response
does not match. Neither check prints the credential.

The local service exposes authenticated endpoints for clients that can change a
base URL:

```text
GET  /openai/v1/models
POST /openai/v1/chat/completions
```

Use the local ContextBridge bearer token and base URL
`http://127.0.0.1:32145/openai/v1`. Model IDs returned by `/models` map to
configured ContextBridge routes; unknown IDs fail instead of falling through
to an arbitrary provider. The surface accepts bounded text/JSON requests and
one validated image input where the selected route proves support. It is a
compatibility boundary, not a claim to implement every OpenAI API feature.

`stream: true` returns one bounded final-result SSE chunk followed by `[DONE]`
unless the selected route proves the narrow native contract described below.
Buffered responses carry `X-ContextBridge-Stream-Mode: final-result` so clients
can detect the exact contract instead of mistaking completion for token
streaming.
Clients that cannot accept this compatibility mode can send
`X-ContextBridge-Require-Stream-Mode: incremental`. ContextBridge rejects that
request with HTTP 409 and `stream_mode_unavailable` **before submitting a job**
unless every v1 condition holds:

- the route has exactly one provider and no fallback chain;
- its engine is `openai_compatible` and explicitly declares
  `incremental_output` in `capabilities`;
- the requested result is plain text without image input; and
- the HTTP writer can flush incremental SSE events.

Example operator opt-in after independently verifying the endpoint's native
SSE behavior:

```yaml
engines:
  reviewed_streaming_api:
    type: openai_compatible
    url: https://provider.example/v1
    remote: true
    model: reviewed-model
    api_key_file: ./secrets/provider.key
    capabilities: [text, incremental_output]
```

Native responses carry `X-ContextBridge-Stream-Mode: incremental`. Every SSE
response also carries `X-ContextBridge-Stream-Resume: unsupported`: a dropped
connection is never silently resumed or replayed as fresh deltas, and a new
HTTP request is a new execution. CB applies
direct downstream backpressure, caps an event at 1 MiB, caps a response at
4096 events and the configured output limit, requires exactly one provider
`[DONE]`, and validates the reconstructed final text before saving it as the
authoritative result. Disconnect/error after partial output closes the stream
without a false `[DONE]`. Requiring `final-result` always keeps the buffered
mode. Fallback, JSON, vision, adapters, cluster transport and E2EE remain
truthfully final-result-only in this first slice.

## Optional LiteLLM gateway

ContextBridge already provides its own authenticated OpenAI-compatible input;
LiteLLM is **not required**. Use this layering only when an existing LiteLLM
deployment should expose a ContextBridge route beside its other models:

```text
application -> LiteLLM -> ContextBridge -> policy, pool and selected engine
```

Generate a secret-free LiteLLM configuration and a separate private
environment file without printing the ContextBridge token:

```sh
contextbridge integrate litellm \
  --write-config ./litellm-contextbridge.yaml \
  --write-env ./.contextbridge-litellm.env
```

Both paths must be new and different. The command refuses to overwrite either
file and removes its newly created counterpart if the command reports that the
two-file operation failed. The YAML refers to `os.environ/CONTEXTBRIDGE_LITELLM_BASE_URL`
and `os.environ/CONTEXTBRIDGE_LITELLM_API_KEY`; only the mode-`0600`
environment file contains the local credential. The generated LiteLLM model
alias is `contextbridge`.

This first integration targets a native LiteLLM process on the same machine as
the loopback-only ContextBridge service. A container's `127.0.0.1` is the
container itself, not the host. Do not expose the local CB listener merely to
make a container reach it; use an explicitly secured and authenticated network
deployment when the gateway runs elsewhere.

Authority remains compositional:

- ContextBridge is authoritative only for requests that LiteLLM actually
  forwards into CB.
- LiteLLM-only routing, retries, budgets and provider calls are outside CB's
  execution evidence.
- `route explain` is an advisory preview. A native CB reservation/submission is
  the authoritative scheduling decision.
- The OpenAI-compatible boundary cannot carry every native CB feature. Durable
  job lifecycle, artifact evidence, E2EE reservations and exact receipts use
  the native contract instead.

See the copy-paste startup and smoke-test commands in
[examples/litellm](../examples/litellm/README.md). LiteLLM's official
documentation describes its [`model_list` configuration](https://docs.litellm.ai/docs/proxy/configs)
and [custom OpenAI-compatible endpoints](https://docs.litellm.ai/docs/providers/openai_compatible).

## External workflow engines

An existing n8n, Temporal, CI, cron or application workflow can remain the
orchestrator and submit bounded native ContextBridge jobs. Use one stable,
producer-scoped `Idempotency-Key` for one logical action and preserve the exact
request bytes across an ambiguous submit retry. Persist the accepted CB job ID
and poll it; never turn a result poll into a new submission.

The dependency-free Node.js reference client demonstrates:

- one bounded repeat after a lost or malformed submit response, using the
  exact same request bytes and operation ID;
- no automatic retry after an HTTP response;
- process-restart recovery with the same operation ID; and
- rejection when different work reuses an existing operation ID.

See [examples/external-workflow](../examples/external-workflow/README.md) for a
copy-paste proof and n8n HTTP Request node mapping. ContextBridge owns admitted
job policy, placement, lease state and finality. The external engine still owns
triggers, branches, approvals, schedules and workflow-level retry policy.

## Model capability passports

ContextBridge keeps two kinds of evidence separate:

- capabilities such as text, vision and embedding; and
- numeric or format limits such as context window, maximum output tokens,
  input-image count, byte budgets and accepted image media types.

Ollama `/api/show` is used as provider evidence for advertised capabilities
and context length. Limits not reported by the runtime remain unknown. For an
OpenAI-compatible or other operator-reviewed engine, declare known facts in
the engine config with `context_window_tokens`, `max_output_tokens`,
`max_input_images`, `max_image_bytes`, `max_total_image_bytes` and
`image_media_types`. The pool publishes both the value and its evidence source.
Known hard limits are enforced before provider execution; missing limits do
not silently become zero or an unsupported claim.

## Externally managed runtimes

An `openai_compatible` engine is always lifecycle-owned by the operator or its
existing runtime manager. ContextBridge discovers its bounded model inventory
and submits configured work; it does not install, start, stop or download
anything for that service. A `llama_cpp` engine with `auto_start: false` has the
same external lifecycle boundary. Only an `auto_start: true` `llama_cpp`
process that CB actually starts is reported with lifecycle owner
`contextbridge` in its runtime status.

Runtime status reports `lifecycle_owner: external` or `contextbridge` where
ownership is known. An external-only configuration no longer inserts or probes
Ollama unless a configured route or fallback actually references `ollama`.
Stopping ContextBridge leaves externally managed services running. Setting
`auto_start: true` on an external API is rejected instead of being silently
ignored.

## MCP stdio

Generate a ready-to-paste generic MCP client entry with the exact executable
and configuration paths:

```sh
contextbridge integrate mcp --json
```

This output contains no ContextBridge bearer token. The MCP client launches
the bounded stdio process itself.

Start the bounded MCP server:

```sh
contextbridge mcp serve --config ./config.yml
```

It exposes four tools:

- `contextbridge.status`
- `contextbridge.cluster_contract_validate`
- `contextbridge.submit`
- `contextbridge.result`

The server does not expose arbitrary shell, filesystem, credential, route
management, schedule management, sampling, or remote-tool registration.
Contract validation is dry-run only. Submission uses the same allowlists,
limits, authentication, and result semantics as other inputs. Operational
status is scrubbed of bearer tokens, credential-shaped fields, and opaque
session-routing keys before it crosses the MCP boundary.

## Read-only UI and observability clients

A dashboard, website backend, trusted desktop application, or alternate
terminal UI should use a dedicated observer identity. Create its private
environment file on the relay host without printing the token:

```sh
contextbridge integrate ui \
  --subject my-dashboard \
  --lifetime-hours 720 \
  --write-env ./contextbridge-ui.env
```

The file contains `CONTEXTBRIDGE_RELAY_URL` and
`CONTEXTBRIDGE_OBSERVER_TOKEN`. It is deliberately read-only: it can inspect
the pool but cannot submit, cancel, pair, drain, resume, change policy, or
issue another token. Keep it in a server-side environment or trusted desktop
secret store. Do not place it in a public browser bundle, mobile binary,
repository, URL, log, or screenshot. A public web interface should call its
own authenticated backend; that backend owns the observer token.

The dependency-free reference client is
[`examples/server-app/contextbridge-ui-client.mjs`](../examples/server-app/contextbridge-ui-client.mjs).
Node.js 18+ can print one bounded snapshot with:

```sh
node examples/server-app/javascript-observe.mjs
```

`ContextBridgeUIClient.snapshot()` reads the protocol manifest, overview,
nodes, bounded job history, and configured pipelines concurrently. Individual
methods expose the same stable native resources without a presentation layer:

```text
GET /v1/cluster/protocol
GET /v1/cluster/overview
GET /v1/cluster/nodes
GET /v1/cluster/events?limit=100
GET /v1/cluster/jobs?limit=50&status=running
GET /v1/cluster/jobs/JOB_ID
GET /v1/cluster/jobs/JOB_ID/events?after=SEQUENCE&limit=100
GET /v1/cluster/jobs/JOB_ID/estimate
GET /v1/cluster/pipelines
GET /v1/cluster/pipeline-runs/RUN_ID
GET /v1/cluster/pipeline-runs/RUN_ID/activity
GET /v1/cluster/pipeline-runs/RUN_ID/events?after=SEQUENCE&limit=100
```

Every response is JSON. The protocol manifest advertises feature and limit
support so clients can degrade honestly instead of guessing by version. Job
and pipeline event pages use `contextbridge.event.v1`, monotonically increasing
cursors, explicit retention gaps, and authoritative/advisory source labels.
For live timelines use the bearer-authenticated SSE endpoints documented in
[execution-events.md](execution-events.md); unlike native `EventSource`, a
backend/desktop `fetch` client can set the Authorization header.

The native HTTP contract is the primary language-neutral SDK boundary. MCP is
an additional bounded tool interface for agent hosts, while the
OpenAI-compatible endpoint targets applications that already speak that
protocol. They share routing and policy but are not interchangeable APIs.

## PHP and shared hosting

On the relay host, create a scoped producer file without printing either the
relay administrator token or the new producer token:

```sh
contextbridge integrate relay \
  --subject my-server-app \
  --lifetime-hours 720 \
  --max-queued-jobs 8 \
  --max-jobs-per-hour 120 \
  --providers ollama \
  --allowed-tenants my-server-app \
  --egress local_only \
  --write-env ./contextbridge-producer.env
```

The destination file must not already exist. It contains only the public relay
URL and the new app-specific producer token. Transfer it through a secure
channel and keep it outside the web root. `--groups private,gpu` can restrict
the token to operator-defined scheduling groups; `--lifetime-hours 0` is an
explicit non-expiring choice rather than the default. The command's terminal
and JSON metadata remain redacted.

Producer governance is part of the issued credential, not a promise made by
the calling application. `--max-queued-jobs` lowers its concurrent queue
share; `--max-jobs-per-hour` is a durable, producer-scoped fixed window that
survives relay restarts; `--providers` is an allowlist; and `--egress
local_only` prevents the credential from widening a local-only boundary. An
exact idempotent replay returns its existing job without consuming another
hourly admission. Current governance deliberately does not claim daily token,
compute, or cost quotas: unknown provider usage is never treated as zero, and
those budgets require reservation-grade usage evidence before enforcement can
be trustworthy.

`tenant_id` is caller-selected unless the credential has an
`--allowed-tenants` scope. One allowed value becomes a safe default; with
multiple values, every request must choose an exact allowed value. Enforcement
precedes execution-policy lookup and returns `scope.tenant_forbidden` for an
out-of-scope label. This scopes an application credential but does not replace
the application's own user authentication. See [Producer identity and tenant
labels](tenancy.md).

`--require-e2ee` can bind that producer credential to sealed native job
admission. The relay rejects cleartext submissions and current cleartext
pipeline runs with the stable `privacy.e2ee_required` code. Do not enable it
for the dependency-free examples below: those deliberately demonstrate the
plain HTTPS job contract and do not implement the one-time E2EE reservation
flow. E2EE hides payload bytes from the relay, not routing, ownership, timing,
usage, or policy metadata. It also does not hide decrypted input from a remote
model provider selected by the worker.

Minimal dependency-free [Python and Node.js server examples](../examples/server-app/README.md)
show idempotent submission, persisted job IDs, bounded polling and terminal
result handling. They are server-side examples, not browser/mobile clients.

[ContextBridgeClient.php](../examples/php/ContextBridgeClient.php) requires
PHP 8.1+, the cURL extension and outbound HTTPS with working certificate trust.
It needs no Composer packages and no CB executable or model on the web host.
It uses producer-scoped idempotency, bounded responses and verified embedded
artifact saves. Keep producer tokens on the server, outside the public document
root; do not ship them to a browser or mobile binary.

[Follow the shared-hosting example](../examples/pool/README.md#3-submit-from-php-shared-hosting)
for a real request, token setup, later polling and optional job controls.
Submission is not completion. Persist the job ID and poll from a later
authenticated application request instead of assuming a long-running PHP
request will survive hosting limits. The example sends plaintext job payloads
over HTTPS; the client does not implement the E2EE reservation/sealing flow.

Run its local regression test with:

```sh
php examples/php/client_test.php
```

## Folder inbox

The folder inbox is the smallest integration for local automation. Producers
write a complete job to a temporary filename and atomically rename it into the
configured inbox. ContextBridge bounds and validates the file before moving it
through the ordinary processor. Partial writes are not treated as jobs.

## Optional adapters

Out-of-tree adapters may expose explicitly configured provider-neutral
endpoints. Their readiness and capability claims are untrusted evidence: the
core bounds them, binds a lease to one endpoint, and verifies returned output.
No vendor-specific adapter ships in this repository. See [adapters.md](adapters.md).
