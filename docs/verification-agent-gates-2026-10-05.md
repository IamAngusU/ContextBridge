# Bounded verification gates — 2026-10-05

## Implemented

- Optional `verification_gate` adapter steps and explicit operator profile pins.
- Exact submitted-input and definition binding on a strict host-result receipt.
- Pass ends the whole run, failure continues only an approved bounded prefix,
  final failure exits nonzero, inconclusive/invalid evidence stops without replay.
- Existing scope, one-attempt dispatch, confirmations, aggregate cost budgets,
  config binding and ungated-plan defaults retained. No code promotion.

## Core checks performed on Windows

- Full `go test ./...` passed.
- Full `go vet ./...` passed.
- Targeted race tests for agent/config paths passed.
- Full command package and config tests repeated on the final candidate.
- Actual CLI binary tested against isolated HTTP transport fixtures: early pass
  dispatched one of three steps; failure then pass dispatched three; exhausted
  failure returned nonzero; inconclusive, wrong input digest and prose stopped
  at one job; missing profile pin rejected the whole plan before any work.
- Existing evidence-selection and named-policy CLI tests passed.
- Negative unit cases cover altered definitions/check IDs, missing/invalid
  counters, duplicate JSON, status mismatches, extra receipt fields, model-step
  gates, changed profile/check across gates, and missing terminal gate.

These transport fixtures do not pretend to be real model or sandbox results.

## Separate real local runtime evidence

An out-of-tree CPython-WASI checker adapter was tested through its actual scoped
adapter-v2 path, a disposable relay and paired worker, with `local_only` policy:

- Known-correct, operator-authored positive control: 10/10 finite cases passed;
  exactly one of three approved check steps ran, two were skipped. This is an
  integration control, not a model coding success.
- Actual local 7B coding-model trial: baseline 3/10, first correction 6/10,
  second correction 6/10. Core stopped nonzero at the approved bound. No code
  was promoted. This model remains unqualified as an autonomous coding default.
- Nine real WASI finite-check controls and nine adapter-v2 conformance checks
  passed; malformed output/early exit remained inconclusive, not false success.
- An initial test using the outdated installed adapter failed startup. The old
  installation was backed up, updated, then the isolated proof was repeated.
  The failed trial was retained rather than reported as a pass.

No existing pool/profile, default model, private application service, or global
GPU policy was changed. Temporary proof processes were stopped. No provider
API was used for inference. `local_only` routing is not a claim of a physically
air-gapped test environment; the WASI guest itself has no network capability.

## Still open

General coding quality, automatic durable resume/compaction, retrieval-aware
memory scheduling, successful recipe qualification and deployment remain
separate work. Finite cases and trusted adapter receipts are not general
correctness proofs or remote attestation. Syntax failures are inconclusive in
the current checker and stop instead of automatically entering another repair.
