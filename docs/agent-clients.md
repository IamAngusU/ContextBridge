# Agent clients, Ollama Launch, MCP, and ContextBridge

Entries such as Claude Code, Codex, OpenCode, OpenClaw, Hermes, Cline, Qwen
Code, Droid, Pi, Copilot CLI, and DeepSeek Harness in `ollama launch` are
applications or agent harnesses. They are not additional models, GPUs, worker
slots, or trusted ContextBridge plugins merely because Ollama can launch them.

ContextBridge therefore never auto-installs or auto-runs that list. A launched
agent may download software, edit files, invoke tools, or inherit credentials;
that deserves the agent's own review and consent.

There are two clean integration patterns:

1. If the client accepts a custom OpenAI-compatible base URL and model, point
   it at `http://127.0.0.1:32145/openai/v1` and a
   `contextbridge:ROUTE` model. See [OpenAI compatibility](openai-compatible-api.md).
2. If the client is an MCP host, configure it to launch
   `contextbridge mcp serve --config ABSOLUTE_CONFIG_PATH`. See the
   [MCP guide](mcp.md).

Use the OpenAI boundary for ordinary chat-completion traffic. Use MCP when the
agent should deliberately inspect ContextBridge status, submit one bounded
job, or read a stored text result. Neither boundary grants arbitrary shell
execution through ContextBridge.

Support still depends on the specific client version. A product name in an
Ollama catalog is not proof that it supports a custom base URL, model alias,
MCP, streaming shape, images, or ContextBridge's route semantics. Confirm that
client's current documentation before presenting it as compatible.

