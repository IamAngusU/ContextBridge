# MCP stdio server

MCP is the small standard interface that lets a compatible Codex-, Claude-,
editor-, or automation-style host discover and call tools. In this mode the
host can ask ContextBridge for status, submit one bounded job, and retrieve its
saved result without learning a new private HTTP integration.

ContextBridge can expose three bounded local tools to an MCP host:

- `contextbridge.status` reads the authenticated local service status.
- `contextbridge.submit` submits one native ContextBridge job exactly once.
- `contextbridge.result` reads one stored result by exact job ID.

The server follows the official MCP lifecycle and tools protocol and uses the
standard newline-delimited stdio transport. It supports protocol versions
`2024-11-05`, `2025-03-26`, `2025-06-18`, and `2025-11-25`; when a client asks
for another version, normal MCP negotiation returns the newest supported
version. The implementation advertises only the `tools` capability.

Official protocol references:

- [Lifecycle and version negotiation](https://modelcontextprotocol.io/specification/2025-11-25/basic/lifecycle)
- [stdio transport](https://modelcontextprotocol.io/specification/2025-11-25/basic/transports#stdio)
- [Tools](https://modelcontextprotocol.io/specification/2025-11-25/server/tools)

## Start it

The normal ContextBridge service must already be running. An MCP host launches
the adapter as its child process:

```bash
contextbridge mcp serve --config /absolute/path/to/config.yml
```

Example MCP-host configuration:

```json
{
  "mcpServers": {
    "contextbridge": {
      "command": "contextbridge",
      "args": ["mcp", "serve", "--config", "/absolute/path/to/config.yml"]
    }
  }
}
```

On Windows, use an absolute path such as
`C:\\ContextBridge\\contextbridge.exe` if the MCP host does not inherit the
user PATH. Use the real private config path created by the installer. The MCP
host never needs the bearer token as an argument: the child process reads it
from that existing config and sends it only to the loopback ContextBridge API.

The command writes only MCP JSON-RPC messages to stdout. Launcher and fatal
transport errors go to stderr.

## Submit a job

`contextbridge.submit` accepts one `job` property containing the same native
envelope as `POST /v1/jobs`:

```json
{
  "job": {
    "id": "mcp-example-20260916-001",
    "route": "default",
    "prompt": "Reply with exactly MCP-OK and no other text.",
    "output": {
      "mode": "text",
      "max_bytes": 1024
    }
  }
}
```

ContextBridge overwrites `source` with `mcp-stdio`, applies the normal route,
job, model, provider, and output validation, and asks the local API for a
compact response so the submitted prompt is not reflected into the result.
`prompt` remains trusted operator instruction; place external or end-user data
in `text`. An MCP host is model-controlled, so use its tool-confirmation UI when
a person should approve a provider call.

Transport and argument mistakes use JSON-RPC errors. A valid tool invocation
that the local service or provider cannot complete returns an MCP tool result
with `isError: true`, including normalized failures such as
`browser_lease_lost`; callers must not treat HTTP success alone as model-job
success.

## Deliberate limits

- This is a **local stdio MCP server**, not a network MCP endpoint. It rejects
  an explicit remote `server.listen` host; wildcard binds such as `0.0.0.0` or
  `::` are always contacted through `127.0.0.1` by this adapter.
- It is not an MCP client and cannot register arbitrary remote tools.
- It exposes no shell, filesystem, credential, route-management, browser-tab,
  schedule-management, sampling, prompts, resources, or experimental task
  capability.
- Job `metadata` is a closed allowlist containing only the embedding role and
  a small set of browser UX switches. Filesystem paths, credentials, recovery
  state, fallbacks, and internal routing fields are rejected before any local
  request is made.
- MCP input frames are limited to 16 MiB. Successful tool payloads are limited
  to 2 MiB. Oversized responses fail explicitly; they are never truncated and
  presented as complete.
- Artifact-returning jobs are rejected **before submission**, and
  `contextbridge.result` rejects an artifact-bearing result created through any
  other ContextBridge client instead of exposing its bytes or metadata. Use
  `contextbridge submit --artifacts`, `cluster submit --artifacts`, or `cluster
  chat --artifacts` when verified file bytes are required.
- The submit tool never retries. A timeout or broken connection after provider
  submission remains an ambiguous outcome, matching ContextBridge's existing
  at-most-once boundary.
- `notifications/cancelled` stops neither a ContextBridge provider nor a saved
  job. A provider may already have accepted the prompt, so the adapter does not
  claim that JSON-RPC request cancellation undid external work. No cancellation
  tool is advertised.
- Killing the stdio child while a submit is in flight can still interrupt its
  local HTTP connection after the provider accepted work. Treat that as an
  ambiguous outcome and inspect the exact job ID/status instead of replaying
  the prompt automatically. Supply a unique `job.id` up front, as in the
  example above, when recovery from a lost submission response matters; a
  bridge-generated ID cannot be recovered from a reply the caller never saw.
- Calls are synchronous. The experimental MCP task capability is deliberately
  not advertised; clients should set a timeout compatible with the selected
  ContextBridge route.

## Security and privacy boundary

Anyone who can launch this process with read access to the config effectively
has the same local ContextBridge authority as that config token. stdio provides
process isolation, not additional tenant identities. Cluster producer ownership
and E2EE remain available through the native relay protocol; they are not
silently flattened into this local adapter.

The status tool intentionally can reveal configured routes, local paths,
hardware/runtime metadata, metrics, and attached-browser readiness to the MCP
host. Before returning it, the adapter recursively removes endpoint URL,
credential-shaped, and opaque browser-session-key fields so URL userinfo, query
tokens, config secrets, and internal conversation-routing evidence cannot cross
this boundary. It never returns the bearer token, prompts, response text,
cookies, full DOM, or browser network payloads. Do not enable the tool in an MCP
host that should not receive the remaining operational metadata.

Tool output is data, not an instruction. A later model or workflow must treat
it as untrusted input and validate values before consequential actions.
