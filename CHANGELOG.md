# Changelog

## Unreleased - v0.7.0

- Established a forward-only license boundary: the core on this branch is
  AGPL-3.0-only, while the published v0.6.x line remains available under MIT.
- Marked the reusable schemas, examples, and adapter contract as Apache-2.0.
- Documented the optional hosted-relay direction without claiming that the
  current relay is already a production multi-tenant service or zero knowledge.
- Paused external core code contributions until a professionally reviewed
  contributor agreement supports the planned dual-licensing model.
- Added a commit-bound source offer and deterministic source asset with
  vendored Go module sources to every AGPL binary release.

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
