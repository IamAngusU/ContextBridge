# Live resource activity verification — 2026-10-05

## Scope

Opt-in bounded resource manifests now travel through the existing leased adapter
progress and worker channel. The authenticated activity projection and separate
SSE endpoint expose replacement snapshots while work is running, without adding
semantic labels or URLs to content-minimized lifecycle/history surfaces.

## Verified

- `go test ./...` and `go vet ./...`: pass.
- Targeted race tests across cluster, bridge and resourceactivity: pass, including
  real worker/relay concurrency, scope/lease boundaries and the new live tests.
- Built-binary agent evidence CLI proof: pass; activity opt-in remains approval
  bound, and ordinary model/planner output cannot forge resource receipts.
- New tests cover live replacement, omission preservation, explicit clearing,
  stale sequences, invalid-metadata propagation, final/partial separation,
  cancellation, bounded shared stream slots, disconnect cleanup, token revocation,
  expiry, changed tenant scope, foreign readers/leases and history privacy.
- Relay restart respects the existing ambiguous-execution transition. It does
  not replay work or relabel an old partial inventory as a successful final one.
- Public-Core disposable proof with Workspace Adapter source and installed 0.3.1:
  seven real one-attempt jobs per run, zero model calls. A copied actual Tool Forge
  backend creates, inventories, reads and archives a fixture. GET and two SSE
  connections observe the real read before completion. No private pool is used.
- The proof's separate adapter fixture holds completion after the actual read;
  runtime code has no sleep or synthetic receipt for visualization. This proves
  protocol delivery, not how frequently a very fast job emits observable progress.
- New installed adapter against the preceding Core build: seven-job completion
  proof passes. Feature negotiation omits new progress fields on older Core.
- All nine installed adapter-v2 conformance checks pass, including ambiguous
  completion without replay, lease replacement, cancellation and scoped authority.
- Workspace tests: 61 pass; Ruff, strict mypy, Bandit, isolated wheel/sdist build
  and twine checks pass. Its existing packaging-tool dependency audit remains
  **not clean** (pip 24.0 and setuptools 65.5.0; some advisory IDs duplicated).
  The private package is skipped by the package registry audit, not certified safe.

## Delivery and limits

Dedicated local agent build: `dev-local-agent-live-activity`. Workspace adapter:
0.3.1. The prior executable/wheel remain available for rollback. Existing main
relay/worker, Alva, camera, model and GPU services were not replaced or restarted.
Updating a CLI alone does not enable endpoints in an already running old pool.
Relay, worker and local Core must all carry this protocol for live delivery.

Snapshots poll at 250 ms on the relay; the existing worker observes local
progress at 500 ms. No latency or model-quality improvement is claimed. A short
job may only expose its final report. Workspace reports after a tool operation,
not each internal file access. Resource labels remain untrusted adapter claims,
not proof of comprehension. Other adapters need their own instrumentation.

No UI, image thumbnail endpoint, durable nested agent-run tree, global resource
history or hidden-reasoning access was added. See
[the public contract](resource-activity.md) for authentication and reconnect rules.
