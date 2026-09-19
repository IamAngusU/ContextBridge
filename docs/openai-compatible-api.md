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
    api_key: ${REMOTE_AI_API_KEY}
    remote: true
    capabilities: [text]
    timeout_seconds: 120

routes:
  reviewed_remote:
    provider: reviewed_remote
    fallback: []
    timeout_seconds: 120
```

Loopback HTTP endpoints are allowed without `remote: true`; non-loopback HTTP
is rejected. Provider keys are omitted from public config JSON/status. The
operator-configured capability list is visible as operator evidence, not as a
benchmark or an inferred intelligence score.

ContextBridge does not send a paid API request merely because an engine exists
in config. A request reaches it only when a job selects a route containing that
engine.

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

