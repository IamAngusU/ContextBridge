# Changelog

All notable changes are documented here. ContextBridge follows semantic versioning.

## 0.5.67 - 2026-09-15

### Fixed

- PowerShell completion preserves one-item command-token collections, so a
  partially typed switch such as `cb selftest --r<Tab>` and a partially typed
  nested action complete correctly instead of only completing after a space

## 0.5.66 - 2026-09-15

### Security

- worker group scopes and paired node public keys remain bound to the approved identity instead of accepting later heartbeat or hello replacements
- E2EE job and result envelopes authenticate their job, producer, node, assignment, and direction context, and result submission verifies the active assignment
- browser jobs re-check the exact authorized conversation before every irreversible action; navigation, cancellation, and uncertain-send recovery fail closed without duplicating prompts
- inbox result creation rejects link/reparse-point targets, browser artifact downloads are origin-bound and streaming-size-limited, and model/runtime downloads enforce bounded authenticated metadata
- per-owner queue and pipeline admission, bounded worker concurrency, retention, pairing cleanup, and fair scheduling prevent one producer or node from exhausting the relay

### Fixed

- browser completions are delivered directly to the relay before using the bounded recovery cache, so valid large artifacts are not rejected by extension storage quotas
- Chromium MV3 reconnect startup now reconciles the actual persistent heartbeat alarm instead of trusting process-local state after worker suspension or browser restart; explicit disconnect and rejected credentials still fail closed
- a fresh ChatGPT or Gemini chat may adopt its permanent conversation URL only after the exact ContextBridge-owned turn is observed; unrelated same-origin navigation is rejected
- completion requires an assistant response after the exact owned user turn, including chats with more than 10,000 rendered message nodes
- diagnostic and progress scripts have deadlines and per-tab in-flight guards, preventing unresolved browser calls from accumulating during provider hangs
- observation-only checks never reload or modify a provider page, and local cache cleanup cannot turn an acknowledged completion into a failure
- local result compaction never restores removed prompts or source inputs, Ollama image generation is distinct from vision, and embedding-only models are never routed for text generation
- ordinary, scheduled, and pipeline work share atomic admission limits; queue scans stay fair when many incompatible jobs precede a routeable one
- scheduler telemetry clamps invalid percentages and free-memory counters, and a model's single-GPU VRAM requirement must fit one GPU rather than the aggregate across cards
- idle relay maintenance checks the small queue and reservation indexes before scanning retained jobs, avoiding repeated decoding of large completed artifacts while preserving startup and active-job recovery
- release checksum manifests use UTF-8 without BOM and LF delimiters on Windows, while the POSIX installer remains compatible with older CRLF manifests

### Improved

- the live terminal accepts commands in foreground `run` and `worker` panels, keeps history newest-first in its own ASCII section, and exposes bounded per-node model/GPU detail views
- terminal status retains readable grouping, load-aware colors, activity animation, zero-GPU nodes, multiple GPUs, node clocks, honest unknown/degraded metadata, and distinct loaded/unloaded local-model colors
- terminal help distinguishes closing an attached view from stopping work; `contextbridge stop` is an authenticated loopback-only, idle-safe shutdown for the standard local service, with an explicit `--force` override
- installers provide a collision-safe, update-stable `cb` shorthand plus PowerShell, bash, and zsh completion; `help` in the terminal panel expands into readable command rows instead of one clipped status line
- `cb selftest` (also `contextbridge cluster selftest`) waits with bounded live progress for local generation, exact attached browser profiles, and free capacity; its default sends nothing, while `--run` uses isolated per-job chats and `--image` separately opts into verified artifact bytes
- `requirements.browser_profile` is a hard scheduler boundary, so N:N pools cannot send a ChatGPT/Gemini-specific job to a worker that lacks a waiting tab of that exact profile
- headless worker installation has explicit noninteractive relay, name, and public-URL inputs with fail-closed validation
- release builds verify stable-version metadata and byte-for-byte extension package parity before atomically publishing a complete artifact set
- the documentation now separates measured protocol limits, performance overhead, compliance readiness, and non-binding roadmap candidates from shipped capabilities

### Tested

- exact and one-byte-over prompt, text, image, output, and aggregate artifact boundaries, including plaintext and E2EE 8 MiB input plus two 6 MiB result artifacts
- Windows live Ollama routing from a VPS with no Ollama UI, minimized browser operation, queue/admission stress, fuzzed relay identifiers, and Linux/macOS cross-builds
- Windows and Linux benchmarks isolate queue, scheduler, encryption, hashing, and artifact-normalization overhead from third-party model latency; an idle production relay with retained large artifacts was re-measured after the maintenance fix

## 0.5.65 - 2026-09-15

### Fixed

- Ollama model capabilities advertised by `/api/tags` now flow through the local runtime and cluster worker, so vision models with opaque names are routed correctly and misleading names cannot override authoritative metadata
- Local cluster model size and VRAM fields use the runtime endpoint's actual JSON names
- Browser text limits are measured in UTF-8 bytes without splitting characters; bounded answers return `truncated: true`, and `cluster chat` warns that the answer is incomplete instead of silently presenting a prefix
- Visual inputs verify their decoded 8 MiB boundary in addition to bounded base64 validation
- Cluster workers no longer echo prompts, source text, documents, queries, or base64 image inputs inside the result envelope, avoiding a second large upload while retaining routing metadata and the actual output
- `cluster chat --attach-image` now requests a vision-capable model, and scheduler modality checks apply to the specifically requested model rather than any other model on the node
- Local and compact cluster responses omit already-known base64 inputs, so a valid maximum-size visual input can coexist with a valid maximum-size returned artifact without overflowing the response transport

### Tested

- Exact and one-byte-over boundaries for 20,000-byte prompts, 200,000-byte text input, 8 MiB images, 64 KiB default output, 1 MiB maximum output, and the 12 MiB decoded artifact budget

## 0.5.64 - 2026-09-14

### Fixed

- Automatically created image-upload chats open visibly so Opera does not indefinitely suspend the upload in an inactive Gemini tab; ordinary text chats remain in the background
- A delayed browser script refuses to click Send after its job deadline

### Added

- `cluster chat --foreground-new-chat` explicitly shows any newly created chat, useful for demos and providers that throttle background work

## 0.5.63 - 2026-09-14

### Fixed

- Gemini image jobs can verify an image-only user turn when Gemini omits the prompt text from its DOM, using the confirmed upload preview, exact retained draft, Send click, one new turn, and its following answer; mismatched visible text and unsafe recovery still fail closed

## 0.5.62 - 2026-09-14

### Added

- Bounded Gemini turn and response-order diagnostics to identify image-job ownership failures without exposing prompts or answers

## 0.5.61 - 2026-09-14

### Added

- `cluster chat --new-chat` and `--new-chat-per-job` request fresh browser conversations directly from the terminal, including the VPS, without reassigning an occupied tab

## 0.5.60 - 2026-09-14

### Fixed

- Gemini OCR image jobs accept the matched user-turn proof from the upload-and-response observer when Gemini omits the older stable turn ID; edit and reload still require their stronger ownership proof
- attached-image jobs hold provisional assistant text until the paired user turn and final answer are verified

## 0.5.59 - 2026-09-14

### Fixed

- Gemini image jobs now require the exact prompt in a new user turn and associate the captured answer with that turn; an upload-only or unrelated answer cannot complete the job
- image jobs fail closed if the completed user turn cannot be verified for session ownership
- follow-up jobs retain a fresh-chat session when its URL changes after completion, but only after checking the stored ContextBridge-owned turn

## 0.5.58 - 2026-09-14

### Fixed

- Gemini image jobs use an already mounted Files input before looking for an upload trigger; a menu with hidden file fields no longer fails merely because its visible button is outside the expected selector path

## 0.5.57 - 2026-09-14

### Fixed

- Gemini image uploads with document-only advisory file-picker filters now require a newly visible image preview before the prompt is entered or sent

## 0.5.56 - 2026-09-14

### Fixed

- Gemini image and carried-file jobs open the Uploads & Tools menu before locating its lazily mounted file input; compatible extension-based `accept` lists now work, and an existing unsent attachment is never replaced
- missing upload controls and already occupied composers produce distinct failure diagnostics without sending the prompt

## 0.5.55 - 2026-09-14

### Fixed

- the extension popup only reports a live connection after a recent successful relay heartbeat; stale stored connection flags show a checking state instead

## 0.5.54 - 2026-09-14

### Improved

- the terminal panel restores color-coded idle, active, warning, and GPU-load states plus a left-to-right job activity bar
- the terminal shows the configured relay separately from its connected worker pool, without implying that a relay-only VPS is a worker
- Gemini model detection inspects only bounded provider controls and keeps unknown selections honest
- Gemini's current mode button is recognized even when it omits `aria-haspopup`; the upload menu is never treated as a model picker
- the local service accepts and bounds the new model-control diagnostics, fixing the 0.5.53 heartbeat rejection
- non-authentication HTTP 4xx heartbeat errors now report a component compatibility problem and retry with backoff after an update instead of falsely blaming the pairing token

## 0.5.53 - 2026-09-14

### Improved

- the extension began reporting bounded model-control metadata for Gemini diagnostics

## 0.5.52 - 2026-09-14

### Improved

- attached ChatGPT and Gemini tabs request a bounded capability refresh after a real user interacts with model or reasoning controls, so the terminal does not wait for the periodic model scan
- the compact ChatGPT composer label (for example, `5.6 Hoch`) updates the visible reasoning level without guessing a model variant; confirmed composer selections win over stale buttons on older answer turns, and background scans leave open menus and unsent drafts alone
- the panel adds vertical breathing room between live sections and provider groups, removing decorative gaps before hiding live state in shorter terminals

## 0.5.51 - 2026-09-14

### Improved

- the read-only attached console now accepts `exit`, `quit`, `q`, or `:q` followed by Enter; leaving that view never stops the managed service
- installation and command-line docs clarify the user PATH, the difference between `console` and foreground `run`, and how to close each kind of window safely

## 0.5.50 - 2026-09-14

### Improved

- the interactive panel is now an in-place live overview with separate ChatGPT, Gemini, other-browser, and local-provider groups; it adapts up to 180 columns instead of wrapping at 78
- session events appear below a HISTORY divider rather than accumulating stale live snapshots above the status line; resizing and zooming redraw the current panel without duplicating terminal rows
- reachable Ollama remains visible even when none of its installed models is loaded; loaded models still precede available unloaded models when present
- classic terminal output and redirected logs retain their previous format

## 0.5.49 - 2026-09-14

### Improved

- terminal AI-tab entries are grouped by provider (ChatGPT, Gemini, then others), with working tabs above idle tabs in each group and a visible per-tab state; only real state changes add an update line
- a separate local-model section appears while at least one local model is loaded, listing loaded models before available but unloaded models within each provider; the attached read-only console uses the same state data

## 0.5.48 - 2026-09-14

### Fixed

- ChatGPT's visible "Too many requests" dialog is detected before draft handling, model selection, or Send; an older successful answer cannot mask the account-level rate limit
- a rate-limited ChatGPT account cools down all attached ChatGPT tabs for five minutes while leaving Gemini available; background model scans do not open menus beneath visible dialogs

## 0.5.47 - 2026-09-14

### Added

- a browser-popup toggle for automatic reconnect after extension/browser restarts and temporary local-service loss; deliberate Disconnect stays off, rejected pairing requires a manual fix, and temporary retries back off

### Fixed

- recurring Chromium worker alarms are no longer reset on every wake, and the local service tolerates a delayed alarm for 90 seconds before declaring an idle browser offline
- browser job pollers pause during a known relay outage rather than repeatedly polling an unavailable service; the popup distinguishes reconnecting from connected

## 0.5.46 - 2026-09-14

### Fixed

- the Chromium service worker now uses a periodic browser alarm to wake after suspension and restore browser heartbeats and job polling; an idle relay no longer requires reopening the popup to reconnect

## 0.5.45 - 2026-09-14

### Fixed

- recovery of a completed answer in a fresh ChatGPT chat now preserves an explicitly empty pre-send baseline; the already visible answer is captured without resubmitting the prompt

## 0.5.44 - 2026-09-14

### Fixed

- ChatGPT answer text nested in a turn's general `.markdown` element is now recognized by both progress sampling and final capture; a visible answer no longer times out solely because its section lacks the older author-role markup
- a stable plain-text answer in an auto-created background ChatGPT tab triggers the existing ownership-checked foreground recovery sooner when the Stop button remains visible

## 0.5.43 - 2026-09-14

### Added

- `contextbridge console`: a read-only live view of the already running service, with the configured panel/classic theme, tab selections, queue, browser state, job totals, and hardware status; no duplicate service process
- Windows Start menu shortcuts for the attached Terminal and local Dashboard

### Improved

- browser Connect confirms the local service and selected tabs before asynchronous page/model diagnostics; slow, inactive, or hung AI pages no longer hold the handshake open
- a registered tab must expose a prompt editor before a job types into it; an unloaded page times out without touching a draft
- bounded diagnostic scripting and network requests; duplicate Connect requests share one attempt, while tab detail scans refresh in the background without blocking heartbeats
- honest popup copy distinguishes a registered connection from page controls that are still being scanned
- command defaults now prefer `config.yml` beside the executable, avoiding a stale second installation's settings; explicit `--config` and `CONTEXTBRIDGE_CONFIG` still win

## 0.5.42 - 2026-09-14

### Added

- selectable `terminal.style: panel|classic` for interactive output; the new default adds a responsive ASCII banner with author/repository link, horizontal event sections, and subordinate job metadata

### Preserved

- the earlier classic presentation remains available, with the same live status data; redirected/service logs keep their timestamped format

## 0.5.41 - 2026-09-14

### Added

- Connect progress shows inspected tabs out of the selected total and a measured ETA when available
- a global or per-job fresh-chat mode, plus opt-in idle cleanup limited to completed ContextBridge-created per-job tabs

### Fixed

- a leftover ContextBridge prompt is identified by a locally salted SHA-256 fingerprint and a page ownership marker, never archived as a user's unsent draft; ambiguous active or attached editors remain untouched

## 0.5.40 - 2026-09-14

### Added

- service, relay, and worker clocks with UTC offsets and signed approximate skew in dashboards and CLI
- bounded schedule history, step outcomes, and live next-run countdown in the local dashboard
- sequential scheduled follow-ups using prior text, JSON, or verified image/file bytes, with untrusted prior output kept outside trusted instructions

### Improved

- extension Connect button now shows a pending animation and blocks duplicate clicks until the attempt finishes
- workflow steps fail before sending when previous data, a verified artifact, or a compatible browser upload control is unavailable; interrupted steps are retained without automatic replay

## 0.5.39 - 2026-09-14

### Added

- durable local schedules and a resource-aware due queue: once, interval, daily, weekdays, weekly, and five-field cron with explicit timezones; authenticated API, CLI, and dashboard controls
- authenticated retrieval of a saved scheduled-job result by job ID
- opt-in ordered browser model/reasoning alternatives, considered only when a visible choice is unavailable before sending
- failure-rate breakdowns by requested provider, model, reasoning, and combined selection in CLI and dashboard

### Improved

- on a proven stalled ContextBridge-created ChatGPT tab, foreground and observe the owned turn without resending before guarded reload recovery
- open scheduled browser chats in the foreground from the start, while preserving background creation for ordinary jobs unless explicitly requested
- keep schedule prompts out of routine status snapshots; persist claimed runs before dispatch and never replay interrupted sends automatically

## 0.5.38 - 2026-09-14

### Improved

- keep a completed browser job authoritative in terminal output while showing an unverified model selection as a separate indented metadata note (`└─`), not as a job failure

### Fixed

- verify and retain the exact submitted turn by ID and digest before a guarded stall reload, even when ChatGPT adds UI text around the prompt; recheck an unchanged response fingerprint before reloading
- report bounded recovery guard reasons without exposing chat or draft content

## 0.5.37 - 2026-09-14

### Fixed

- use the same ChatGPT composer pill for model selection as for the successful capability scan, even when unrelated menu buttons appear earlier in the form

## 0.5.36 - 2026-09-14

### Fixed

- select ChatGPT models from a composer menu that is already open, rather than toggling it closed; report bounded, text-free diagnostics when a requested choice is missing

## 0.5.35 - 2026-09-14

### Fixed

- find ChatGPT's current composer menu even when the prompt input sits outside its form; never mistake a model label on an older answer for the current selection

## 0.5.34 - 2026-09-14

### Fixed

- apply an explicit ChatGPT model preference through the current composer's pointer-driven model submenu, checking the exact visible model before sending; an unavailable model still fails closed

## 0.5.33 - 2026-09-14

### Fixed

- retry ChatGPT model discovery promptly up to three times when a fresh tab is scanned before its composer menu appears, then back off to the normal five-minute cadence

## 0.5.32 - 2026-09-14

### Fixed

- support composer model menus that open on pointerdown or mousedown before falling back to an ordinary click; report which activation path was used
- reject model scans of non-AI tabs in the background worker as well as in the popup

## 0.5.31 - 2026-09-14

### Fixed

- prefer the active tab of the Opera window that opened the extension popup, even when the extension can list active tabs from other windows; an explicit tab-list selection still wins
- when Scan model choices has a stale non-AI selection, scan the active ChatGPT or Gemini page instead of requesting permission for an unrelated site such as Bugcrowd
- report bounded, selector-only model-scan diagnostics in the local browser status so failed auto-detection can be investigated without copying page or chat contents

## 0.5.30 - 2026-09-14

### Fixed

- follow the current ChatGPT composer's model-and-effort submenu header when the first popup contains only a reasoning slider; scan only controls newly exposed by that popup and never select a model or alter the private draft
- retry a still-unknown ChatGPT model immediately after an extension update instead of carrying over the previous version's five-minute scan delay

## 0.5.29 - 2026-09-14

### Fixed

- write the transient Windows status directly into its existing console row and leave the cursor anchored; zooming, narrowing, or reading scrollback no longer relies on carriage-return/erase redraws that can multiply fragments
- redraw an idle timer only once per second instead of every animation frame
- allow a safe ChatGPT model scan of an unfocused background tab even when its editor still retains focus internally and contains a private unsent draft

## 0.5.28 - 2026-09-14

### Fixed

- identify the unlabeled ChatGPT composer menu beside the file-attachment button and scan its asynchronously rendered model/effort choices; retry missing ChatGPT models every five minutes without modifying a private draft or interrupting a focused editor
- recalculate terminal width on every status redraw, keeping the animated line inside the current viewport after CMD zoom instead of adding wrapped lines

### Added

- stable provider and modality indicators in the terminal and cluster dashboard, with unadvertised modalities gray and no assumed media support
- node-load percentage, explicit GPU ready/active/Zero-GPU state, day/hour idle duration, and a display-only node discriminator; `worker --name` offers a session-only rename while `cluster.worker.node_name` remains persistent

## 0.5.27 - 2026-09-14

### Fixed

- scan the current ChatGPT composer menu (including its "Denkaufwand" pill) for model choices instead of the "Modell wechseln" action on an older answer
- wait for asynchronously rendered menu choices and include plain button entries; a zero-result scan now shows a bounded trigger diagnostic without collecting chat content

## 0.5.26 - 2026-09-14

### Fixed

- do not print a new tab line when an unchanged ChatGPT tab temporarily stops exposing its model or reasoning control; real selection changes are labeled as updates
- detect ChatGPT's icon-only "Modell wechseln" control and read the selected model from its menu when a safe idle scan is possible, without touching a draft or active response

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
