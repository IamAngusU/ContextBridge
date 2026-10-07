# Bounded agent evidence and result contracts — verification

## Delivered

- Explicit `input_steps` for non-adapter steps, carrying selected earlier results
  and the original task as untrusted submitted content. Source step/job identity
  and content digests are retained, without treating them as correctness proofs.
- Hash-bound selection and ordered provenance; no implicit whole-history import.
- Bounded run-local storage, 128 KiB encoded aggregate limit, and visible failure
  instead of partial-context forwarding.
- Optional model `output_mode: json`, backed by the actual provider output
  contract, with strict result-envelope checks. Asking for JSON in prose alone
  is deliberately not treated as a machine-readable guarantee.
- Legacy data flow, adapter request shapes, local-only policy, tenant/group,
  budgets, confirmations and one-attempt dispatch remain unchanged.

## Automated checks

On Windows:

- `go test ./...` (including the explicit external-binary CLI proofs).
- `go vet ./...`.
- Targeted race tests for agent evidence/output, automatic and named-policy
  execution, preflight, proposals and planner guidance.
- Negative cases: unknown/forward/self/duplicate dependencies, mixed input
  modes, adapter input selection, missing/corrupted/over-limit evidence,
  multi-byte text, encoded expansion, changed approval, truncated results,
  unsupported output modes, fake JSON carried as text, mixed result envelopes,
  duplicate JSON properties and text conflicting with a JSON adapter handoff.
- CLI proof: preview performs no work, changing selected inputs invalidates the
  approval, and the approved execution includes exactly the selected sources
  without unrelated prior results or widened authority.

These fixture-driven CLI tests verify transport and policy, not model reasoning.
Platform-independent Go code is not a claim of a separately executed Linux or
macOS installation proof.

## Actual local-model / adapter proof

A separate disposable loopback relay, paired worker, installed workspace
adapter and copied Forge backend were used. The operator seeded synthetic task
state and a separate JSON file, then discarded that setup runner. The agent's
adapter profile was read-only, permitted only `task.read` and
`workspace.inspect`, and was bound to one synthetic workspace. No live pool
configuration, customer data or persistent deployment permission was changed.

The plan had three explicit steps: read saved memory, inspect the separate file,
then combine both with `input_steps`. One planner job ran before review; the
three work jobs ran only after an independent exact-plan audit and hash approval.
Every durable job was single-attempt and classified `local_only`. The returned
randomized release identifier and integer revision had to equal operator-known
fixture values, with both source IDs named.

Observed failures were retained rather than reported as success:

- A local Qwen2.5 generalist failed data-flow planning in three development
  probes: it selected only the immediate predecessor, or combined incompatible
  input modes. These plans did not execute work. This remains negative evidence
  about planner reliability, not a fixed issue or a general success-rate estimate.
- One disposable-pool startup ended with a connection reset before model work;
  it was cleaned up and is not counted as a model inference failure.
- Qwen2.5-Coder 7B Q4_K_M planned the selected-source flow, but a text-contract
  answer wrapped the requested JSON in Markdown. The strict end-to-end check
  failed. This motivated the explicit `output_mode` contract instead of stripping
  arbitrary prose and calling the result verified.
- With that JSON contract, three separately seeded full flows passed: the
  candidate, installed and final installed builds. Their wall times were about
  29.6, 20.7 and 20.6 seconds, including startup, pairing and model loading;
  these are not steady-state latency benchmarks.

Final verified Windows binary SHA-256:
`f3e5d8ab896795563dd03a08bc669817f7d47ec3552f85b7649ca8d982bdc8fb`.

No online AI was used; the proof endpoints were loopback and the jobs had local-
only policy. This is not an OS-level network-isolation/air-gap test. Test services
were terminated and the test model unloaded afterward. No default model change
was made.

## Still open / next useful work

1. An explicitly authorized memory read/update lifecycle across runs, with
   project identity, conflict detection and operator-visible todo/pin retention.
2. Provider/token-aware context budgeting and selective compaction. A byte cap
   alone cannot guarantee that a provider will not clip its own context window.
3. Typed conditional verification gates: stop after genuine success and repair
   only on actual test failure, within approved attempts. No blind retries.
4. More independent multi-file coding and source-grounded retrieval cases before
   qualifying an autonomous coding model or promoting generated recipes.

This slice does not automatically make every `do` request remember prior chats,
read private files, execute shell commands, resume a run, or deploy generated code.
