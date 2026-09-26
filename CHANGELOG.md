# Changelog

## Unreleased

- Added credential-bound E2EE admission. Producer credentials issued with
  `--require-e2ee` reject every cleartext job and the currently unsupported
  cleartext pipeline path with stable `privacy.e2ee_required` evidence. The
  protocol manifest advertises the boundary, durable store admission enforces
  it below HTTP, and documentation explicitly distinguishes payload secrecy
  from zero knowledge: routing, ownership, timing, policy and usage metadata
  remain relay-visible.
- Extended passive portable `service` endpoint metadata with bounded
  loopback-only capability and execute paths plus typed-execution, workflow,
  and artifact-lineage capability labels. These fields make richer local
  toolboxes discoverable without granting permission to launch or call them;
  unsafe paths and attempts to attach service-only fields to model APIs remain
  rejected.
- Made the panel command composer state-aware instead of a static help block.
  It now validates the configured prompt-character bound and the authenticated
  relay's advertised encoded-payload limit while typing, shows incomplete
  input in yellow and invalid input in red, derives provider/model/profile/
  reasoning choices from the live service snapshot, removes already used or
  incompatible next flags, and sends the exact typed routing options only
  after the same parser accepts them. The configurable
  `terminal.max_prompt_characters` guard defaults to 4096, while the relay
  remains authoritative for its byte limit. Compact `RLY`, `WRK`, `UPD`,
  `RAG`, `PCK`, and engine-autostart (`EAS`) indicators now expose meaningful
  effective feature state in the service section (green on, dim off).
- Fixed the panel command editor hiding a newly typed leading or trailing space
  until the next character. The command area now preserves editor whitespace,
  shows compact context-sensitive syntax while typing, keeps command feedback
  visually distinct from hints, treats empty Enter as a no-op instead of
  expanding the full help wall, and folds lifecycle/stop guidance into one
  concise footer.
- Turned `contextbridge console` into a bounded interactive pool client when an
  explicit scoped producer credential is available. Its typed `send`, `jobs`,
  `job`, `result`, and `cancel` actions reuse normal relay admission, ownership,
  policy and event APIs; never inherit relay-admin authority; never execute host
  commands; remain disabled for redirected/non-TTY input; cap automatic event
  followers; keep prompt/result content out of global history; and preserve
  plaintext idempotency and honest cancellation/E2EE wording. Without a
  producer credential the same console stays read-only. The console,
  `cluster chat`, and `cluster submit` now share the same typed text-job and
  submit/read client core instead of drifting across separate wire contracts.
- Added bounded performance-aware placement. Successful fenced jobs build a
  relay-owned smoothed runtime estimate per node and opaque execution route;
  after a configurable minimum sample count it becomes one additive routing
  signal alongside live slots, queue, CPU/GPU/RAM/VRAM pressure, loaded-model
  state, affinity and failures. Evidence expires, outliers are limited,
  unknown workers remain neutral, workers cannot forge it through heartbeats,
  and `route explain` exposes the exact samples, estimate, age and score.
  Relay-derived coarse load contexts additionally learn separate curves for
  slot/queue, CPU/GPU, RAM/VRAM, adapter pressure and model warmth, while the
  route-wide estimate remains a neutral fallback until a context is proven.

## v0.8.0 - 2026-09-25

- Added `contextbridge guide` and opt-in `cluster configure --interactive` for
  bounded, TTY-only device-role setup. The guide resolves explicit flags,
  existing config, supported environment values and safe defaults before it
  asks for missing input; then shows a redacted provenance summary and writes
  only after confirmation. Pipes, CI, MCP, services and redirected I/O never
  prompt, and secure pairing remains a separate explicit trust step.
- Added a first-class secure offline-LAN bootstrap with persistent Ed25519 TLS
  identity, TLS 1.3, a public join bundle, exact relay-origin SPKI pinning and
  explicit pairing. Loopback administration remains separate, generic LAN
  cleartext stays rejected, and discovery is not treated as trust.
- Added relay-owned failure-aware placement. Matching node/provider/model
  routes receive a bounded recent-failure penalty, open a short exponentially
  capped circuit after repeated transient failures, recover through a
  successful probe, and expose stable evidence through `route explain`.
  Producer-triggered evidence is isolated by an opaque producer scope, while
  relay-observed node disconnects remain global. Heartbeats cannot forge or
  clear the state, while policy, budget, artifact, cancellation, capacity, and
  planned-drain failures do not poison it.
- Added an authenticated Prometheus-compatible `/metrics` endpoint for admin
  and observer credentials. It exports only fixed-cardinality pool aggregates
  and deliberately omits tenant, job, node, provider, model, prompt, and error
  labels.
- Added a reversible `client`/`sender` cluster role for devices that may submit
  and observe work but must not advertise worker capacity. Role changes retain
  the bounded worker identity for a later opt-in return, require a safe relay
  URL, preserve scoped-token revocation as a separate authority boundary, and
  take effect after an explicit service restart. Installers now infer worker
  mode from a supplied relay URL, accept equivalent Windows environment hints,
  expose sender-only setup, reject ambiguous role hints, and document both
  checked-out/ZIP installation and the pairing-preserving one-line path.
- Added `cluster conformance resilience`, a provider-free reproducible proof
  for monotonic restart authority, idempotent lost-response recovery, explicit
  post-dispatch ambiguity, stale-fence rejection and single durable terminal
  results. The versioned JSON report states its narrow scope instead of
  presenting local store checks as network or HA evidence.
- Rejected HashiCorp Raft as the HA release basis while its reported
  asynchronous-heartbeat safety violation remains open, selected the
  deterministic `etcd-io/raft` v3.7.0 core instead, and added a pinned
  CB-specific partition proof that a former leader's minority proposal never
  enters the committed stream. This is a dependency gate, not an unsupported
  claim that the relay integration is already HA.
- Added an explicit native macOS 26 arm64 Actions job. Beyond tests and the
  race detector it builds and executes the native binary, performs an isolated
  checksum-verified install, validates both LaunchAgents, starts the loopback
  service, and verifies ordinary uninstall preserves configuration.
- Hardened the cross-platform gate after its first independent fork run:
  canonical runtime paths are compared correctly on macOS and Windows,
  slower hosted runners receive bounded but realistic integration deadlines,
  every new static-analysis exception carries local evidence, native lifecycle
  assertion failures name the missing invariant, and Actions are pinned to
  current Node 24-based releases. The vulnerability gate is also pinned to a
  Go 1.27-compatible `govulncheck` instead of crashing inside an obsolete
  analyzer.
- Fixed the parallel worker integration proof's failure cleanup so a missed
  progress deadline first releases blocked local handlers, cancels the worker,
  and only then closes its HTTP/WebSocket servers. A slow Windows runner now
  reports the original bounded assertion instead of hiding it behind a
  ten-minute `httptest.Server.Close` deadlock; the timing gate was widened to
  tolerate verified slow-host evidence without becoming unbounded.
- Added real incremental text output for explicitly reviewed
  `openai_compatible` engines with the `incremental_output` capability. The
  v1 path has no fallback ambiguity, applies direct backpressure and hard
  event/byte bounds, requires `[DONE]`, reconstructs and validates the final
  text, and keeps every unproven/E2EE/cluster path in honest `final-result`
  mode.
- Marked every compatibility SSE response explicitly non-resumable so a client
  cannot mistake a fresh request for continuation or replay of an interrupted
  execution.
- Added the first authoritative execution-event plane: relay-owned job
  lifecycle events commit atomically with admission, assignment, execution
  start, retry, cancellation, terminal completion and ambiguous failure.
  Producer-scoped cursor reads are bounded, retained per job, report replay
  gaps explicitly and expose coordination-safe lifecycle for E2EE jobs.
  `cluster events JOB_ID` exposes human, JSON and reconnect-safe follow modes.
  Worker progress is separately marked advisory, strips semantic text/detail,
  cannot create terminal state, and is capped at 64 observations per job.
- Extended the same authoritative cursor contract to pipelines. Run and step
  transitions commit atomically with their owning state, step events retain
  stable run/step/child-job identities, producer ownership is enforced, and
  `cluster events RUN_ID --pipeline --follow` survives reconnects without
  deriving state from logs.
- Added `contextbridge integrate openai|mcp`: it emits redacted, exact
  application connection settings, can create a new non-overwriting private
  OpenAI-compatible environment file, and generates a ready-to-paste MCP stdio
  entry without exposing a bearer token. Its non-executing OpenAI preflight
  distinguishes reachability, authentication and route advertisement; a real
  bounded inference smoke request requires an explicit `--live` flag. Relay
  operators can create a scoped, expiring producer environment file without
  exposing either administrator or producer secrets in terminal output, and
  dependency-free Python/Node examples cover the durable server-app flow.
- Added wire-v3 assignment fencing: a stable durable cluster identity,
  monotonically increasing relay process epochs, and exact per-assignment
  generations now bind dispatch, cancellation, progress, completion, and slot
  release. Workers persist the highest accepted epoch and reject older or
  foreign relay authority before model execution. This is a pre-consensus
  safety primitive; it does not claim multi-relay failover or Raft consensus.
- Accepted an implementation-gated HA architecture: an opt-in, quorum-backed
  single-writer FSM with explicit replicated-state, membership, migration,
  snapshot, dependency, and failure-proof boundaries. The selected consensus
  core is pinned for tests only; no runtime HA support claim is included yet.
- Worker identity and accepted relay epochs are now flushed to a temporary
  same-directory file and atomically replaced. A failed replacement cleans up
  its temporary file instead of risking a partially written durable fence.
- Assignment-fence validation now rejects corrupt non-positive persisted
  attempts before any signed-to-unsigned conversion, preventing a negative
  attempt from aliasing a very large generation.
- Local result reads are confined with `os.Root`; even a filesystem-level
  symlink planted inside the managed jobs directory cannot make the authenticated
  result endpoint read outside that directory.
- OpenAI-compatible clients can require native incremental streaming with
  `X-ContextBridge-Require-Stream-Mode: incremental`. Until a route can prove
  that capability, ContextBridge rejects the request before job submission
  instead of silently degrading an execution to buffered final-result SSE.

## v0.7.3 - 2026-09-24

- Added durable administrator-controlled worker drain/resume state for planned
  maintenance. Drain survives reconnects and relay restarts, excludes the node
  at the scheduler and assignment transaction, and never cancels, migrates, or
  silently replays work that already owns a lease.
- Added separate content-minimizing `/livez`, `/readyz`, and `/leaderz` probes
  for reverse proxies and future relay failover. The current role is explicitly
  `standalone`; quiescing or an unavailable durable store fails readiness and
  writability closed without pretending that consensus already exists.
- Advertised both boundaries through the protocol manifest as
  `durable_worker_drain_v1` and `relay_role_health_v1`.

## v0.7.2 - 2026-09-24

- Isolated panics at worker-job, pipeline, schedule, inbox, and terminal-event
  boundaries so one malformed work item cannot terminate the whole process.
  Ambiguous execution remains terminal and is never silently replayed.
- Made the bounded OpenAI-compatible SSE behavior explicit through the
  `X-ContextBridge-Stream-Mode: final-result` response header and integration
  documentation; native token-delta streaming is not claimed.
- Added independently verifiable, Ed25519-signed offline execution receipts
  bound to the exact stored job/result evidence instead of trusting an online
  relay comparison alone.
- Made configured `max_attempts` operative only for structured, proven
  pre-execution worker refusals; post-start and ambiguous failures stay
  terminal.
- Enforced bounded-agent cost authority as an aggregate run budget and bound
  approvals to the effective execution configuration.
- Kept the worker's local bearer token on validated loopback URLs and reduced
  sealed provider failures to stable relay-visible codes.
- Preserved the exact local stop response during asynchronous shutdown and
  covered the callback handoff without relying on timing sleeps.

## v0.7.1 - 2026-09-22

- Standardized `cluster chat`, console, and worker terminal output on clear
  English labels instead of leaking hard-coded German text on non-German
  systems.
- Added locale-independent regression coverage for request, model, reasoning,
  route, status-panel, node, GPU, and model-state labels.
- Moved the declared, CI, security, race, and release-build toolchain from the
  unsupported Go 1.25 line to the maintained Go 1.27 line.

## v0.7.0 - 2026-09-21

- Added a public Ed25519 verifier for issuer-backed interoperability
  statements that bind exact subject bytes, ContextBridge source, scope,
  evidence digests, and a maximum 366-day validity window without placing an
  issuer private key in the distributable binary.
- Kept free self-run conformance distinct from the future `ContextBridge
  Verified` service and documented why verification expires by time while
  technical evidence remains bound to an exact version and artifact.
- Corrected the security policy's stale MIT-only description after the
  forward-only AGPL license boundary.

- Established a forward-only license boundary: the core on this branch is
  AGPL-3.0-only, while published releases v0.6.0 through v0.6.3 remain
  available under MIT.
- Marked the reusable schemas, examples, and adapter contract as Apache-2.0.
- Documented the optional hosted-relay direction without claiming that the
  current relay is already a production multi-tenant service or zero knowledge.
- Paused external core code contributions until a professionally reviewed
  contributor agreement supports the planned dual-licensing model.
- Added a commit-bound source offer and deterministic source asset with
  vendored Go module sources to every AGPL binary release.
- Made the AGPL release builder reject every version below v0.7.0 so an
  accidental v0.6.4 cannot cross the documented license boundary.
- Restored the canonical ContextBridge wordmark to the public landing page and
  documented the public core by capability instead of relying on source-code
  discovery.
- Published reproducible Windows and Linux bridge-only latency, throughput,
  resource-footprint, payload-limit, and exclusion evidence without folding
  model or Internet latency into the numbers.
- Added public operational guides for pooling, placement, schedules, bounded
  agents, integrations, portable resource packs, and explicit limits.
- Added distribution regression tests that require the brand and core guides
  while preventing private out-of-tree adapter names from entering public text
  surfaces.
- Defined accurate, versioned compatibility statements for producers, relays,
  workers, resource packs, and out-of-tree adapters, backed by the existing
  protocol manifest and no-inference conformance commands.
- Reframed the public introduction around the plain-language outcome—one
  governed pool for resources the operator already controls—without claiming
  certification, adoption, or standard status that has not been earned.
- Added `contextbridge uninstall` with dry-run planning, ownership-checked
  integration cleanup, data-preserving defaults, and an explicit bounded
  `--purge` mode that preserves external or ambiguous paths.
- Clarified that the dated shared-VPS benchmark can contain uncontrolled
  noisy-neighbour variance and is not evidence of an operating-system gap.
- Hardened Linux resident-memory measurement by rejecting an invalid
  non-positive operating-system page size before unsigned conversion.
- Added a strict installer-ownership manifest so optional packages can register
  their own program paths without exposing package details in the public core.
- Rejected filesystem roots and the user home as purge authority, and added a
  Windows post-uninstall verification report for any owned item left behind.
- Bound shell-completion cleanup to the exact owned install path or command
  name, so uninstalling one installation cannot alter another installation's
  profile block or completion files.

## v0.6.3 - 2026-09-20

- Added bundled third-party license and notice material to every release.
- Added reviewed SPDX license evidence to SBOM components and made release
  generation fail closed when a new runtime dependency lacks that metadata.
- Documented fair use of the ContextBridge project identity so forks can give
  accurate credit without implying that modified products are official.

## v0.6.2 - 2026-09-20

- Made release archives deterministic from a clean Git commit and
  `SOURCE_DATE_EPOCH`, including normalized ordering, timestamps, ownership,
  and file modes for ZIP and tar.gz outputs.
- Added a CycloneDX 1.5 `SBOM.cdx.json` to every platform archive.
- Added a machine-readable `BUILD-PROVENANCE.json` that records the source
  commit, build inputs, reproducibility controls, and artifact digests.
- Kept the trust boundary explicit: the current build record is unsigned and
  is not presented as publisher identity or a third-party attestation.

## v0.6.1 - 2026-09-20

- Fixed the Windows installer so `-NoPath` can intentionally create a custom
  launcher without treating its absence from `PATH` as a collision.

## v0.6.0 - 2026-09-20

- Established the public ContextBridge core as a vendor-neutral local-first
  execution fabric.
- Added local and API engines, authenticated worker pools, capability-aware
  scheduling, durable jobs, verified artifacts, E2EE, schedules, MCP, execution
  receipts, and bounded agents.
- Defined a generic out-of-tree adapter contract. Implementation-specific
  adapters are not distributed in this repository.
- Hardened integer and size boundaries, queue ownership, execution-policy
  binding, resource-pack identity, partial writes, update state, and installer
  command collisions.

This repository begins with a clean public history. Private pre-release history
is intentionally not part of the public distribution.
