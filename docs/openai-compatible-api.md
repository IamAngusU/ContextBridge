# OpenAI-compatible clients and providers

ContextBridge 0.5.75 adds two separate, deliberately bounded compatibility
boundaries:

1. applications can call ContextBridge through a small OpenAI Chat
   Completions surface; and
2. a ContextBridge route can call a reviewed OpenAI-compatible provider.

They are not an unrestricted proxy. Native ContextBridge jobs remain the
authoritative interface for cluster requirements, E2EE, artifacts, schedules,
pipelines, route explanations, and detailed failure states.

## Use ContextBridge from an application

Start the normal service, then configure the client with:

```text
Base URL: http://127.0.0.1:32145/openai/v1
API key:  the private server.token from config.yml
Model:    contextbridge:default
```

Discover route-backed model IDs:

```bash
curl -fsS http://127.0.0.1:32145/openai/v1/models \
  -H "Authorization: Bearer $CONTEXTBRIDGE_TOKEN"
```

Submit a completion:

```bash
curl -fsS http://127.0.0.1:32145/openai/v1/chat/completions \
  -H "Authorization: Bearer $CONTEXTBRIDGE_TOKEN" \
  -H "Content-Type: application/json" \
  -d '{
    "model":"contextbridge:default",
    "messages":[{"role":"user","content":"Reply exactly API-OK"}]
  }'
```

Every `contextbridge:NAME` maps to an existing configured route named `NAME`.
An omitted model uses `default`. Authentication is mandatory, the listener is
loopback-only by default, and responses are marked `no-store` by the normal
service middleware.

### Shipped alpha surface and limits

- `GET /openai/v1/models`
- `POST /openai/v1/chat/completions`
- one completion (`n=1`)
- text or `json_object` output
- non-streaming JSON or final-result SSE (`stream:true`); this is not provider
  token-by-token streaming
- at most 128 messages and 128 parts per message
- 20,000 trusted instruction characters and 200,000 history characters
- one final-user image as a base64 data URL, at most 8 MiB decoded
- no remote image fetches
- no tool calls; use the bounded local [MCP adapter](mcp.md)
- no compatibility `/embeddings`, `/responses`, fine-tuning, files, batches,
  assistants, or provider account APIs yet

System/developer instructions and the final user request become the trusted
task instruction. Earlier user, assistant, and tool messages are retained as
explicitly untrusted submitted history. Each request gets a fresh browser
session when a browser route is selected, so an OpenAI-style stateless client
does not accidentally inherit a personal conversation.

## Route to an OpenAI-compatible provider

Remote prompt egress is fail-closed: HTTPS, `remote: true`, a resolved key, an
exact model, and explicit capabilities are all required.

```yaml
engines:
  reviewed_remote:
    type: openai_compatible
    url: https://your-reviewed-provider.example/v1
    model: your-exact-model-id
    api_key_file: ./secrets/remote-ai.key
    remote: true
    capabilities: [text]
    timeout_seconds: 120

routes:
  reviewed_remote:
    provider: reviewed_remote
    fallback: []
    timeout_seconds: 120
```

`api_key_file` is resolved relative to `config.yml` unless it is absolute. It
must be a regular, non-symlinked file of at most 16 KiB containing exactly one
non-empty secret line. On Unix, group/other permissions are rejected. On
Windows, protect the file with an ACL granting only the account that runs
ContextBridge. `api_key` and `api_key_file` are mutually exclusive. A resolved
file secret is held in memory for provider requests but is excluded from YAML
and public JSON serialization, so a later config save cannot copy it into
`config.yml`.

Loopback HTTP endpoints are allowed without `remote: true`; non-loopback HTTP
is rejected. Provider keys are omitted from public config JSON/status. The
operator-configured capability list is visible as operator evidence, not as a
benchmark or an inferred intelligence score.

ContextBridge does not send a paid API request merely because an engine exists
in config. A request reaches it only when a job selects a route containing that
engine.

### Guard a paid compatible provider

For providers with a same-origin balance endpoint and reviewed token prices,
an engine can refuse work before generation when the remaining balance would
cross an operator-set floor. This DeepSeek example uses a local key file and
the documented peak prices reviewed on 2026-09-19:

```yaml
engines:
  deepseek:
    type: openai_compatible
    url: https://api.deepseek.com
    model: deepseek-flash
    api_key_file: ./secrets/deepseek.key
    remote: true
    capabilities: [text]
    max_output_tokens: 1024
    reasoning_effort: low
    balance_path: /user/balance
    minimum_balance_usd: 5
    costing:
      mode: upper_bound
      source: "DeepSeek peak pricing reviewed 2026-09-19"
      input_per_million_usd: 0.30
      cached_input_per_million_usd: 0.006
      output_per_million_usd: 1.20
```

Before submitting, ContextBridge reads the provider balance, reserves a
conservative upper bound for every concurrent request in the worker process,
and checks `balance - reservations >= minimum_balance_usd`. The reservation
uses UTF-8 input bytes as a deliberately conservative token ceiling, the
configured maximum output, peak non-cached input pricing, and peak output
pricing. It does not assume a prompt-cache discount. Image input is rejected
under a balance floor until its cost can be bounded safely.

This is a spend guard, not an invoice or a cross-account ledger. Independent
worker processes using the same provider account do not share in-memory
reservations, provider balances may update asynchronously, and taxes or other
provider charges may differ. Use one controlled provider gateway or a larger
floor when several processes share a key. The completed job records the
reviewed source, reservation, token usage when returned, and a conservative
cost status. Missing monetary evidence remains `unknown`; ContextBridge never
turns it into `$0`.

The `balance_path` must be a path on the already configured provider origin;
queries, fragments, protocol-relative URLs, and another host are rejected.
DeepSeek's official references are the [balance
endpoint](https://api-docs.deepseek.com/api/get-user-balance/), [pricing
table](https://api-docs.deepseek.com/quick_start/pricing/), and [chat
completion fields](https://api-docs.deepseek.com/api/create-chat-completion/).

## Offline Arsenal example

Offline Arsenal already speaks the OpenAI Chat Completions protocol. Point it
at a local ContextBridge route instead of coupling either project to the
other's internals:

```powershell
$env:ARSENAL_LLM_BASE_URL = 'http://127.0.0.1:32145/openai/v1'
$env:ARSENAL_LLM_API_KEY = $env:CONTEXTBRIDGE_TOKEN
$env:ARSENAL_LLM_MODEL = 'contextbridge:portable_local'
pnpm start
```

The demonstrated path is:

```text
Offline Arsenal -> authenticated ContextBridge route -> hot-plug ModelKit -> Ollama model
```

On 2026-09-19 that local chain returned the exact marker
`ARSENAL-CONTEXTBRIDGE-MODELKIT-OK`. This is a dated integration observation,
not a universal latency or compatibility promise.
