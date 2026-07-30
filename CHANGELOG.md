# Changelog

All notable changes are documented here. ContextBridge follows semantic versioning.

## Unreleased

### Added

- Visual browser teaching for prompt, submit, response, and optional image controls
- Separate ready-to-load extension packages for Chromium and Firefox
- Local dashboard with browser heartbeat, routes, queue state, and recent activity
- `contextbridge dashboard` command with token handoff through a URL fragment
- Cross-platform extension packaging and verification scripts
- Linux systemd user service and macOS LaunchAgent setup paths
- Hardware, RAM, VRAM, CUDA, ROCm, Metal, and local engine status in CLI and dashboard
- Named Ollama and `llama.cpp` engines with explicit GPU fallback policy
- Verified `llama.cpp` runtime installation from official release assets
- Resumable GGUF model downloads with Hugging Face LFS SHA256 verification
- Native extraction, embedding, `rag_ingest`, and `rag_query` tasks
- Tenant-separated local vector store behind a replaceable interface
- Persistent route, task, provider, model, flag, latency, failure, and embedding metrics
- Authenticated external tunnel heartbeat endpoint
- `status`, `hardware`, `models`, `pull`, and `runtime install` CLI commands

### Changed

- Browser host access is requested only for the selected page origin
- Open browser jobs can use a visually taught local profile while YAML remains available
- Failed browser result delivery is retained and retried instead of rerunning the page job
- Ollama can select a compatible installed model when configured with `model: auto`
- Provider and model telemetry now comes from trusted routing config, not model output

## 0.1.1 - 2026-07-14

### Changed

- Hardened release checksum verification and cross-platform installers
- Improved external job inputs and pairing behavior

## 0.1.0 - 2026-07-14

### Added

- Local HTTP and folder job inputs
- Ollama text and image provider
- Explicit browser-tab provider
- Ordered provider fallback
- InkWall moderation adapter
