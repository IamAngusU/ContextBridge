# Changelog

All notable changes are documented here. ContextBridge follows semantic versioning.

## 0.5.8 - 2026-09-13

### Added

- explicit per-tab attachment and detachment, plus a detach-all control; merely viewing a tab no longer authorizes it
- optional auto-attachment only for new, empty ChatGPT/Gemini chats after two DOM checks and an existing origin grant
- Gemini model/mode discovery from the live mode picker, including localized choices such as Flash variants
- a documented list of unreproduced provider pause and safety-check edge cases

### Fixed

- the one-click browser helper no longer attaches existing conversations just because they are recently active
- tab selection and attachment can be changed while the browser bridge is running

## 0.5.7 - 2026-09-13

### Added

- a root-owned systemd timer can update a least-privileged relay installation while the relay itself remains unable to modify `/usr/local/bin`
- `update auto --managed-service contextbridge-relay.service --relay-only` checks relay idleness, validates the release, restarts the systemd unit, confirms its versioned health endpoint, and rolls back on failure
- `CONTEXTBRIDGE_UPDATES_EXTERNAL=1` disables only the unprivileged in-process update loop; the configured automatic-update preference still controls the privileged timer

## 0.5.6 - 2026-09-13

### Fixed

- the one-click browser setup now picks one recently active ChatGPT tab and one Gemini tab, instead of filling the 16-tab limit with historical ChatGPT conversations

## 0.5.5 - 2026-09-13

### Added

- automatic release checks remain enabled by default, but activation waits for an idle local bridge, worker, and relay; the separate Windows update task also checks live service health
- staged releases must pass a version and current-config self-test; Windows rolls back if the restarted service fails its versioned health check, and Unix-like services keep a pending rollback until healthy
- a failed release is quarantined against repeated automatic attempts; `updates.enabled: false` in the config always wins
- the extension can select all open ChatGPT and Gemini tabs with one click, adding parallel slots to an already-running connection

### Fixed

- Gemini account-activity controls are no longer misreported as the current AI model
- update downloads require both GitHub asset digests and SHA256SUMS, and redirects remain HTTPS-only

## 0.5.4 - 2026-09-13

### Fixed

- ChatGPT image-only turns and Gemini response containers are recognized as complete responses, with answer text kept separate from turn controls
- same-origin image and file references get another bounded download attempt in the extension background worker
- browser file jobs can require verified transferred files with `output.min_artifacts` or `cluster chat --min-artifacts`; text claims and URLs alone no longer pass this requirement
- `cluster chat --image` asks for an image in the prompt and checks transferred image bytes rather than accepting text, links, or code files; explicit image-tool selection remains optional
- image-mode jobs wait for rendered images and report provider limits or missing image tools as failures
- image-generation percentage widgets stay in the progress state instead of being streamed as repeated answer text
- required-artifact jobs now fail promptly after a stable text-only answer; hidden ChatGPT file inputs accept an attached local image plus prompt text
- `browser inspect` reports a bounded, local-only DOM selector snapshot without chat or file contents
- terminal and bridge logs show collected files separately from provider-hosted references
- Windows installer updates extension files in place, preserving the directory selected by an unpacked browser extension

## 0.5.3 - 2026-09-13

### Added

- interactive workers show the latest compact browser-output iteration in place, with deterministic rotation across parallel jobs

### Fixed

- long progress text respects the current console width instead of producing scrolling status lines
- pricing and unpin controls containing the word "model" are no longer reported as AI model selectors

## 0.5.2 - 2026-09-13

### Fixed

- remounted React nodes with an old assistant turn can no longer be accepted as a new browser response
- a generation that ends without a new assistant turn reloads once and resumes without submitting the prompt twice
- account/profile controls and unrelated labels no longer appear as detected model or reasoning choices

## 0.5.1 - 2026-09-13

### Added

- optional E2EE for terminal chat through `--e2ee` and interactive `/e2ee on|off`
- CPU frequency/load, operating-system build, machine uptime, agent version, and GPU driver telemetry
- colored interactive terminal states while preserving plain redirected logs and `NO_COLOR`

### Fixed

- ChatGPT jobs now finalize when the stop control disappears and the composer is ready, even when the empty composer hides its send button
- localized profile-menu labels are no longer misidentified as model or reasoning selections

### Changed

- CPU utilization now contributes to multi-worker scheduling, including zero-GPU jobs

## 0.5.0 - 2026-09-13

### Added

- Multi-tab ChatGPT/Gemini pooling in one extension, with private session-to-conversation affinity
- determinate image-generation progress and controlled recovery after navigation or a demonstrably stalled page
- per-job and per-session browser profile, model, and reasoning selection with localized menu matching
- automatic inventory of ready Ollama, GGUF, ONNX, and SafeTensors models with inferred capabilities and resource estimates

### Changed

- Rate limits, provider failures, page busy state, and valid final answers are now separate states; error UI is never accepted as successful model output
- series responses can return up to twelve images/files within a verified 12 MiB transfer budget
- workers report RAM generation/speed, GPU utilization/temperature, browser slot load, and richer multi-PC pool metrics
- idle terminals use compact `[Idle …]`, slot, node, and hardware status groups while retaining bounded long polls and cached probes

## 0.4.4 - 2026-09-13

### Added

- `contextbridge cluster chat`, a streaming terminal conversation with automatic browser-session affinity
- producer-scoped `requirements.session_id` routing so follow-up turns prefer the same browser worker without disabling failover
- bounded response artifacts for generated images, download links, and code files, with SHA-256 normalization and safe local materialization
- `submit --artifacts` and `cluster submit --artifacts`; interactive chat saves artifacts automatically

### Changed

- Reconnect log floods are now an animated terminal status with an indeterminate progress bar, compact lifecycle events, and deduplicated non-interactive retry logs
- Zero-GPU scheduling and parallel worker placement remain available while ordered worker preferences are deterministic
- Local provider errors now fail the cluster job instead of being mislabeled as successful; interactive browser turns default to one attempt to avoid duplicate UI submissions

## 0.4.3 - 2026-09-13

### Fixed

- Successful jobs now finalize the last progressive browser snapshot atomically as `final` and not busy

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
