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

`stream: true` currently returns one bounded final-result SSE chunk followed
by `[DONE]` after the routed job completes; it does not expose incremental
provider token deltas. Such responses carry
`X-ContextBridge-Stream-Mode: final-result` so clients can detect the exact
contract instead of mistaking buffered completion for native token streaming.
Clients that cannot accept this compatibility mode can send
`X-ContextBridge-Require-Stream-Mode: incremental`. Until a selected route can
prove native ordered deltas, ContextBridge rejects that request with HTTP 409
and `stream_mode_unavailable` **before submitting a job**. Requiring
`final-result` is also accepted. This opt-in negotiation prevents a caller from
discovering the mismatch only after remote work has already executed.

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

## PHP and shared hosting

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
