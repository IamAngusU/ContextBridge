# ContextBridge documentation

The public repository contains the provider-neutral execution core. Optional
adapters are separate operator-installed executables and are not bundled or
described as vendor integrations here.

## Start and operate

- [Architecture](architecture.md): trust boundaries and data flow.
- [Security](security.md): threat model, defaults, and limitations.
- [Operations](operations.md): the shortest path from install to a local job,
  a worker pool, and repeatable health checks.
- [Pools and placement](pools-and-placement.md): topologies, capacity,
  capability evidence, and failure semantics.
- [Automation](automation.md): schedules, pipelines, and verified follow-ups.
- [Portable resources](portable-resources.md): hot-plug packs identified by a
  stable manifest rather than a drive letter.
- [Limits and performance](limits-and-performance.md): exact byte/count
  boundaries, benchmark method, measured latency, throughput, and footprint.

## Automate and integrate

- [Send a pool job and keep control](../examples/pool/README.md): shared-hosting
  PHP, real text/JSON requests, per-job choices, operator policy and file limits.
- [Compatibility boundaries](compatibility.md): versioned job, relay, worker,
  adapter, and resource-pack claims plus commands for collecting evidence.
- [Verification](verification.md): free self-run conformance and the signed,
  version- and time-bounded statement format for future reviewed verification.
- [Bounded agents](bounded-agent.md): manual approval, local-only auto mode,
  and operator-owned authority envelopes.
- [Application integrations](integrations.md): native jobs, OpenAI-compatible
  input, MCP, PHP, and the folder inbox.
- [Provider-neutral adapters](adapters.md): out-of-tree endpoint contract.
- [Hosted Relay readiness](hosted-relay.md): what is and is not ready for a
  managed coordination service.
- [Job Contract v1 schema](schemas/job-contract-v1.schema.json): reusable
  machine-readable submission boundary.
- [Verification Statement v1 schema](schemas/verification-statement-v1.schema.json):
  portable signed-review envelope.
- [Verification Trust Key v1 schema](schemas/verification-trust-key-v1.schema.json):
  portable issuer public-key document.

## Build and verify

- [Supply chain](supply-chain.md): deterministic archives, SBOMs, source
  bundles, checksums, and current signature limitations.
- [Licensing](../LICENSING.md): AGPL core and Apache-2.0 exception boundaries.
- [Security reporting](../SECURITY.md): private vulnerability channel.

Run `contextbridge help` for the complete command list and
`contextbridge <command> --help` for flags. The README deliberately presents a
small happy path; these documents state the hard boundaries behind it.
