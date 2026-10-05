# Resource activity verification — 2026-10-05

## Delivered scope

Public, provider-neutral `output.activity` / resource manifest contract,
approval-bound agent `--activity`, authenticated job activity projection, typed
OpenAPI response, feature discovery and first optional workspace producer.
No UI, private deployment dependency, global browsing history, extra filesystem
authority or hidden reasoning export.

## Tests

- Complete `go test ./...` and `go vet ./...`: passed on Windows/amd64.
- Targeted `go test -race` across resourceactivity, bridge, cluster and CLI:
  passed. Includes activity validation/projection, producer tenant boundaries,
  scoped observers and agent evidence flow.
- Built-binary CLI plan/run proof: passed. `--activity` persists in the approval
  hash and is requested only by work jobs, not the planner. Changed plan input
  is rejected before execution; work output prints the per-job activity URL.
- Projection tests exercise persisted evidence across relay restart, foreign
  producer/missing-resource 404 equivalence, anonymous rejection, scoped
  observer/tenant restrictions, no-store, explicit opt-in, pending/missing/
  invalid evidence and sealed input/result exclusion.
- Strict manifest tests cover duplicate/unknown fields, invalid enum pairs,
  controls/bidi characters, host paths, URL credentials/queries/fragments,
  malformed hashes/refs, count limits and JSON-escaping amplification.
  Malformed optional evidence does not turn a completed mutation into an error.
- Workspace adapter: 60 unit tests, Ruff, strict mypy and Bandit passed.
- Installed adapter-v2 conformance: all 9 checks passed, including expiry,
  cancellation, generation/capability rotation and ambiguous completion without
  replay. The conformance job requested activity in its completion envelope.
- Real isolated Core -> relay -> paired worker -> adapter-v2 -> Tool Forge:
  seven one-attempt jobs, six end-to-end assertions passed for source and then
  installed adapter. Actual file creation, content read versus inventory, ZIP
  creation versus reuse, opt-out and content-minimized lifecycle events were
  checked. Zero model calls; no GPU load. All proof processes and temporary
  state were cleaned up. This is not an OS network-isolation benchmark.

## Local installation boundary

The dedicated local agent executable and workspace adapter wheel were updated,
with the previous executable and 0.2.0 wheel retained for rollback. Normal Core
and speech services and their running pool were not replaced or restarted. A
running relay/worker/local Core also needs the new contract to expose it there;
installing only a CLI does not upgrade those processes. Private configuration,
Alva, camera, GPU policy and existing customer/project data were left alone.

Final dedicated Core build: `dev-local-agent-activity`, SHA-256
`97593f402b666fde90cecd88a939333876562de060a78ebe9d754c340c31a618`.
The installed workspace adapter is 0.3.0. The live source activity schema is
documented in [resource-activity.md](resource-activity.md).

## Limitations and audit warning

Resource entries arrive with completion, not as individual live events. The
existing lifecycle SSE remains available. Actual per-resource live streaming,
thumbnail serving, durable agent-run/subagent trees, schedule-card aggregation
and instrumentation of research/vision/other adapters remain separate work.
Receipts are explicitly adapter-reported; Core cannot prove semantic truth.

The installed workspace Python runtime dependency audit reports pre-existing
known vulnerabilities in pip 24.0 and setuptools 65.5.0 (some scanner rows repeat
IDs). They were not silently changed in this functional patch. The private
adapter is absent from PyPI and skipped by that scanner. Isolated wheel/sdist
building and twine checks pass; this is **not** a clean whole-environment security
audit or an unrestricted production-readiness claim. Linux/macOS were not tested.
