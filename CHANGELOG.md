# Changelog

All notable changes are documented here. ContextBridge follows semantic versioning.

## 0.5.25 - 2026-09-13

### Fixed

- recheck the exact session tab, submitted ContextBridge user turn, empty composer, absent unsent attachments, and closed edit dialogs immediately before any automatic recovery reload; a changed tab or personal draft now fails closed
- recover a pre-existing ChatGPT stale Stop only when the last ContextBridge-owned turn has a visible completed answer, Stop is disabled, and the answer fingerprint remains unchanged for 30 seconds; reload once and verify the same chat before sending the next job
- detect an accepted text prompt that still has a disabled Stop but no new answer after 105 seconds, then use the same guarded, resume-only recovery without submitting it twice

### Added

- local browser diagnostics for whether a visible Stop button is disabled or still spinning, without collecting prompt or response text

## 0.5.24 - 2026-09-13

### Fixed

- leave existing ChatGPT drafts untouched when a stale Stop control indicates the provider is still busy, including if that state appears between draft capture and clearing
- do not stream an older Gemini answer while editing temporarily removes the last answer from the conversation

## 0.5.23 - 2026-09-13

### Fixed

- reject media and artifact jobs in the text-only edit mode before opening a provider's Edit dialog; an older generated file must never be mistaken for this job's output
- preserve an unsent personal draft or attachment when editing a previous message, and show which attached tabs have edit mode enabled

## 0.5.22 - 2026-09-13

### Added

- opt-in, per-tab text prompt replacement for a verified last ContextBridge-owned ChatGPT or Gemini message, with stable message identity and content hash checks; prior/new attachments and intervening edits fail closed

### Fixed

- never mistake ChatGPT's still-visible Stop control for Send or type a new prompt into a page that is still generating
- defer automatic Windows installation while a manual terminal is open or the managed task is not running; the scheduled updater retries without quarantining a healthy release
- stop displaying an old rollback marker as the current error after a newer healthy core has been installed

## 0.5.21 - 2026-09-13

### Fixed

- explain when a manual Windows update cannot replace a ContextBridge terminal that is still running, without stopping it or claiming the new version succeeded
- allow a manual update performed after closing that terminal to verify the new executable without waiting for a service that only the user can restart

## 0.5.20 - 2026-09-13

### Changed

- start with automatic updates disabled; users can opt in from the local dashboard, extension, or CLI, while an explicit config `false` remains a lock
- reserve separate ChatGPT/Gemini conversations per producer, session, and provider; allow manual return to an exact known chat URL, opt-in automatic new-chat tabs, or a per-job new-chat trigger

### Fixed

- preserve worker-derived session scope through the local browser queue, so equal public session IDs from different producers do not collide
- attempt one bounded resume-only reload when a new Gemini plain-text response stops changing but its Stop control remains active

## 0.5.19 - 2026-09-13

### Fixed

- match browser model labels with Unicode spaces, including Gemini's non-breaking spaces, against ordinary spaces typed by users; local model identifiers remain exact
- wait for Gemini's animated Music menu to render and reuse it if already open, then verify that Music remains selected before submitting the prompt
- include Gemini's visible Music/Image tool checkboxes in the local, content-free browser inspector so a failed tool lookup can be diagnosed without sharing a full DOM dump

### Verified

- parallel ChatGPT and Gemini text jobs, E2EE follow-ups, unknown-model rejection before Send, a local Ollama job, a transferred ChatGPT PNG, and a transferred Gemini Music MP4 with decodable non-silent AAC audio on a live Windows worker

## 0.5.18 - 2026-09-13

### Fixed

- scope Gemini's Quill text replacement to its prompt editor; `selectAll` could select the entire page after a locally preserved draft was cleared, leaving the new prompt unsent
- recover a plain ChatGPT text turn once if the Stop control stays visible despite 90 seconds of unchanged answer text; reload and verify the same conversation without resending the prompt, while image/media jobs and disabled auto-reload remain untouched

## 0.5.17 - 2026-09-13

### Fixed

- emit provisional Gemini text only from a newly created assistant turn, not from a prior Music player's changing timer while the next job is generating

### Added

- `cluster chat --min-images N` and `/min-images N` require exactly the requested minimum of verified image files, including multi-image follow-ups with `--attach-image` as a visual reference
- optional, bounded local draft preservation before browser jobs: save the exact unsent text with tab/provider/time to a private user-home JSONL history, then clear only an unchanged editor; saving or clearing failures leave the job unsent

## 0.5.16 - 2026-09-13

### Fixed

- permit explicitly attached provider pages' HTTPS artifacts to be fetched by the extension background; the extension CSP previously blocked these reads even when page access had been granted
- inspect ChatGPT image file-card previews when a new assistant turn contains no inline image or downloadable link; keep the image job failed unless actual image bytes transfer
- distinguish Gemini's visible Music selection from an incompatible leftover tool, and recognize its generated media player as a file rather than plain answer text

### Added

- `cluster chat --music` selects Gemini's visible music tool and requires a transferred audio or video file; `output.min_media` provides the same verified-file requirement for JSON jobs
- allowlisted, permission-gated retrieval of Gemini media from `contribution.usercontent.google.com`; the local service checks the declared media type against decoded file bytes before accepting it

### Known limit

- third-party pages may expose a file card without a readable preview URL, or media larger than the bounded 12 MiB transfer budget; those jobs remain failed with a clear artifact error rather than returning a text claim as a file

## 0.5.15 - 2026-09-13

### Fixed

- keep Connect available when the selected tab-list row has no profile; ChatGPT and Gemini are recognized from the current page without manual provider selection
- validate the saved pairing token against the authenticated local service before marking the browser bridge connected
- require a successful browser heartbeat before Connect reports success, and show connection errors separately from stale website-job failures
- recover a reloaded Opera tab when its AI prompt is actually ready, even if the browser has not reported the tab as fully loaded
- withhold provisional browser text unless a live Stop or streaming control is visible, so a stale Gemini answer does not leak into the next job's terminal stream

### Changed

- reduce the default popup to current-page attach/detach and one Connect/Disconnect action; move multi-tab management, model scanning, profile teaching, and diagnostics under Advanced
- let Connect explicitly attach the current recognized AI page when no tab was attached yet, so a separate profile or local-service test is not part of normal setup
- default fresh-tab auto-attachment to off for new users; it remains opt-in under Manage other tabs and existing saved choices are preserved

## 0.5.14 - 2026-09-13

### Fixed

- classify ChatGPT's retry banner on the newly failed user turn as `browser_provider_error`, without mistaking older answer text for an account rate limit
- let Gemini image-only turns finish when the requested number of real images has loaded, the response is stable, and only a stale `aria-busy` flag remains
- keep image-generation placeholder text out of progressive assistant text; file transfer and verified image counts remain separate progress states
- reject Gemini's explicit peak-hour fallback notice for a Pro-requested turn without labeling it an account rate limit; suppress that turn from progressive assistant text
- keep ChatGPT's thinking-only chrome out of progressive answer text

### Added

- report the number of fully loaded images in the content-free per-tab DOM heartbeat
- offer a one-click attach/detach action for the page beneath the extension popup, independent of the selected tab-list row

## 0.5.13 - 2026-09-13

### Added

- sample content-free DOM status for attached tabs even during an active job: busy indicator types, last response length, and whether that response is still marked busy; no prompt or answer text is included in the heartbeat

### Fixed

- recognize Gemini's empty, ready composer as a completed turn once a fresh response has stayed stable for six seconds, even though the Send button disappears
- finish a Gemini turn when only a stale `aria-busy` flag remains after a new answer has stayed unchanged for twelve seconds and no Stop, streaming, or image-generation signal is visible
- make `cluster chat --artifacts off` disable artifact collection in the job as well as local saving, avoiding an unnecessary five-minute browser wait
- reserve time before the local route deadline to finalize a browser result, and report `browser_timeout` instead of masking it as `providers_unavailable`

## 0.5.12 - 2026-09-13

### Added

- show each attached tab's currently visible model and reasoning level in the worker console, updating only when the selection changes
- label the model and reasoning requested for each job separately from the selection reported after a successful browser run; show the same distinction in terminal chat
- expose content-free attached-tab selection and state in worker capabilities for relay monitoring

### Fixed

- do not present a requested browser model as the confirmed model in the terminal; only the extension's successful selection confirmation is labeled as tab-reported
- reject Gemini output if the visible mode changes from the requested model family before sending or before accepting the answer (for example Pro falling back to Flash)

## 0.5.11 - 2026-09-13

### Changed

- use native select-all/insert-text in Gemini's Quill composer, make only one insertion attempt per job, and require an exact normalized draft before clicking Send
- leave a mismatched or unrelated draft untouched rather than appending retries or submitting it

The live Gemini Pro completion test remains pending until the operator clears the unsent test draft and reloads the extension.

## 0.5.10 - 2026-09-13

### Added

- live All/Attached/Not attached tab filtering across windows of one browser, with working and cooldown state in the extension popup
- safe automatic Gemini mode discovery on an idle attached tab with an empty draft; no provider page reload
- per-worker task, provider and model allow-lists enforced on the PC, plus session-only `--slots` and Windows `--topmost`
- separate `--relay` and `--identity` overrides for pairing and worker sessions so one PC can join independent servers with distinct policies

### Fixed

- clear an incompatible selected Gemini tool before a plain-text job and rebind the composer after the tool switch
- recognize Gemini's localized "Nachricht senden" control, rebind a composer replaced while typing, and never overwrite an unrelated unsent draft
- report selected Gemini tools, prompt presence/length, and a whitelisted failure reason in the content-free local DOM snapshot
- keep browser-tab capacity separate from local-model worker capacity when scheduling parallel jobs

See [multiple-server setup](docs/multiple-servers.md) for the current per-process hardware-budget limitation.

## 0.5.9 - 2026-09-13

### Fixed

- recover orphaned updater locks after a short helper-safety grace period; status remains responsive during an update check
- rebind Gemini's prompt editor after a model switch, verify the entered prompt, and fail promptly if the send button stays disabled
- keep visible provider rate-limit/error messages out of progressive answer text

### Verified

- live Gemini mode discovery on a German page reports the account's four offered choices; requests must use their exact visible labels (for example `3.1 Pro`, not just `Pro`)

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
