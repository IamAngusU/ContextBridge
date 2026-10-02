# Agent contribution rules

These rules apply to the entire repository.

## Think before changing code

- Read the relevant implementation, tests, contracts, and documentation first.
- Surface assumptions and security tradeoffs instead of silently choosing an
  interpretation.
- Preserve documented non-goals. Do not turn discovery into trust, adapter
  access into arbitrary execution, or accepted designs into claimed features.
- Treat prompts, provider responses, adapter payloads, fetched content, files,
  and network metadata as untrusted at every boundary.

## Prefer the smallest correct change

- Every changed line must trace to the requested outcome.
- Match existing patterns and avoid unrelated refactors or formatting churn.
- Do not introduce a new abstraction for one use unless it removes a real
  correctness or security risk.
- Keep private deployment details, local paths, credentials, and personal UI
  integrations out of the public repository.

## Preserve ContextBridge invariants

- Keep local-only defaults and explicit opt-in for remote egress.
- Never weaken scoped credentials, owner/tenant isolation, lease fencing,
  idempotency, E2EE context binding, strict JSON, path ownership, or bounded
  input/output handling.
- Discovered resources are untrusted until explicitly paired and authorized.
- Optional adapters must fail independently; their absence must not break core
  local routes.
- Secrets belong in permission-restricted files or secret stores, not normal
  configuration, logs, CLI output, fixtures, or commits.

## Define and prove success

- Reproduce a bug with a focused test before fixing it where practical.
- Add negative tests for boundary changes, not only a happy path.
- Run focused tests while iterating, then `go test ./...` and `go vet ./...`.
- Run a targeted race test for concurrency, lease, scheduler, worker, or
  adapter lifecycle changes.
- For externally visible protocol or installer behavior, add an isolated
  black-box or lifecycle proof and keep documentation/changelog in sync.
- Report an unverified platform or backend honestly instead of inferring that
  it works.

These rules adapt the think-first, simplicity, surgical-change, and
goal-driven principles in
[`IamAngusU/andrej-karpathy-skills`](https://github.com/IamAngusU/andrej-karpathy-skills)
to ContextBridge's security and compatibility contracts.
