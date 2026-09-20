# Changelog

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
