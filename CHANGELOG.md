# Changelog

All notable changes are documented here. ContextBridge follows semantic versioning.

## 0.4.2 - 2026-09-13

### Fixed

- Built-in ChatGPT and Gemini profiles can be verified in a fresh chat before the first assistant response exists
- Browser profile verification now explains when the response target will appear after the first answer

## 0.4.1 - 2026-09-13

### Fixed

- Windows service and update tasks now use the installation directory explicitly
- Windows autostart and updates are no longer blocked or stopped on battery power

## 0.4.0 - 2026-09-13

### Added

- Automatic ChatGPT and Gemini profiles derived from current DOM and accessibility anchors
- Progressive browser answer snapshots across extension, local bridge, worker, and relay
- `contextbridge cluster submit --stream` while preserving final stdout output
- Non-interactive Windows worker setup through `-RelayUrl` and `-NodeName`

### Changed

- Browser completion detects new response elements, localized stop controls, busy state, and stable completion
- Visual teaching is now an override and custom-provider path rather than a mandatory ChatGPT/Gemini step
- Windows installation waits for a healthy local bridge before opening the dashboard
- Releases are built and verified locally with `scripts/build-release.ps1`; GitHub Actions workflows were removed

### Security

- Progressive plaintext is never forwarded for E2EE jobs

## 0.3.1 - 2026-09-13

### Fixed

- Windows installs add ContextBridge to the user and current-session `PATH`
- Unix installs print an explicit `PATH` instruction when the selected binary directory is not already available

## 0.3.0 - 2026-09-13

### Added

- `contextbridge doctor` with actionable config, service, route, relay, and worker checks
- Provider-aware cluster requirements, including explicit routing to ready browser workers
- Cross-platform Go CI on Windows, macOS, and Linux
- End-to-end relay/worker concurrency coverage with a real WebSocket and local provider boundary

### Changed

- Relay dispatch now reserves worker slots immediately and strictly honors `max_concurrent`
- Historical VRAM demand is a GPU preference while CPU-only workers remain eligible
- Workers advertise only live local providers and include local queue depth in scheduling signals
- Worker hardware discovery is cached briefly to avoid serial GPU/CPU probes after parallel jobs
- Browser result capture waits for common generation-busy indicators to clear
- Relay scheduling uses authoritative in-flight slots instead of waiting for the next heartbeat
- Dependencies and GitHub Actions were updated to their current maintained major versions

### Fixed

- New worker connections can no longer be marked offline by cleanup from the replaced connection
- Windows runtime extraction now rejects rooted archive paths
- Empty decision flags remain `[]` across the browser completion boundary
- YAML-based browser profiles now advertise readiness correctly to cluster relays

## 0.2.0 - 2026-07-30

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
- Durable single-relay compute clustering for 1:1, 1:N, and N:N producer-worker topologies
- Outbound WebSocket workers with pairing codes, scoped credentials, groups, capability discovery, and VRAM-aware scheduling
- Priority queue, history, bounded retries, disconnect recovery, resource learning, token usage, compute cost, and savings metrics
- Optional X25519 and AES-256-GCM E2EE payloads bound to a reserved worker
- Declarative multi-model pipelines with fixed routes, JSON templates, retries, timeouts, and bounded loops
- Embedded one-file cluster dashboard and `relay`, `worker`, `pair`, `run`, and `cluster` CLI commands
- Core-managed automatic updates with a dashboard toggle, daily OS jobs, checksum and asset-digest verification, staged execution, atomic activation, and rollback
- Public ContextBridge welcome page with cached GitHub stars, release download totals, and privacy-preserving unique installer counts

### Changed

- Browser host access is requested only for the selected page origin
- Open browser jobs can use a visually taught local profile while YAML remains available
- Failed browser result delivery is retained and retried instead of rerunning the page job
- Ollama can select a compatible installed model when configured with `model: auto`
- Provider and model telemetry now comes from trusted routing config, not model output
- Browser completion delivery now copies mutable decision data at the synchronization boundary

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
