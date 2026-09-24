# Changelog

## Unreleased

- Added wire-v3 assignment fencing: a stable durable cluster identity,
  monotonically increasing relay process epochs, and exact per-assignment
  generations now bind dispatch, cancellation, progress, completion, and slot
  release. Workers persist the highest accepted epoch and reject older or
  foreign relay authority before model execution. This is a pre-consensus
  safety primitive; it does not claim multi-relay failover or Raft consensus.
- Accepted an implementation-gated HA architecture: an opt-in, quorum-backed
  single-writer FSM with explicit replicated-state, membership, migration,
  snapshot, dependency, and failure-proof boundaries. No Raft dependency or
  HA support claim is included yet.
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
