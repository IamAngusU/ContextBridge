# Optional bounded agent verification gates

An agent plan may opt in to stop-on-pass checks. This is a small conditional
extension to the existing finite, approved step list, **not** an open-ended
agent loop or automatic code deployment. Old plans/configs remain unchanged.

## Operator setup and trust

An adapter profile must provide its usual `agent_instruction_contract` and the
reserved option `agent_verification_checks`: a map of 1..8 safe check identifiers
to `sha256:`-prefixed lowercase definition digests. The adapter's documented
definition format determines the digest. Core does not invent tests or interpret
provider-specific suite data. IDs match `^[a-z][a-z0-9_-]{0,39}$`.

The operator is explicitly trusting this scoped adapter's host-side verifier.
Only the check IDs and the existing instruction contract enter planner prompts;
suite contents, private options and digests do not. The full effective profile
remains execution-binding-bound, so changing checks requires a new plan/review.

No profile is enabled automatically, and no live permissions are widened.
Gates do not roll back side effects of earlier approved steps. Use a staged
candidate/check workflow when existing workspace contents must stay untouched.
Normal tenant/group, egress, cost, lease, confirmation and adapter-v2 boundaries
still apply. A compromised trusted adapter/worker can lie: a receipt is **not**
remote attestation, a sandbox-escape proof, or proof about arbitrary inputs.

## Plan field

An adapter step may contain:

```json
"verification_gate": {"check": "acceptance-v1"}
```

Only IDs pinned on that step's allowed profile are accepted. Every gate in one
plan must use the same profile and check. A gated plan must end with a gate.
All possible steps undergo local preflight before the first dispatch, including
steps that may be skipped. Existing maximum steps/runtime/cost still bound the
worst case. Gate fields are included in hash approval.

| Receipt outcome | Effect |
| --- | --- |
| `passed` | Finish the whole run successfully; skip all remaining steps. |
| `failed` | Continue only the next preapproved step. If final, exit nonzero. |
| `inconclusive` | Exit nonzero; no speculative repair or replay. |
| Missing/invalid receipt, timeout, partial result or execution error | Exit nonzero; no replay. |

For example: `check → repair → check → repair → check`, at most five steps.
Passing the first check executes just one job. Passing the second executes
three. There is no jump backwards, added work, success-only side effect, resume,
automatic retry, or hidden model call. If a summary is wanted after success,
request it separately: gate success ends the **whole** plan.

## Adapter result contract

The contracted adapter must return a complete bridge JSON output envelope
(`mode=json`, nonempty `json`, no `text`, `error` or truncation). In its result
object it adds exactly this receipt shape under `agent_verification`:

```json
{
  "schema": "contextbridge.agent-verification.v1",
  "check": "acceptance-v1",
  "definition_sha256": "sha256:<64 lowercase hex digits>",
  "input_sha256": "sha256:<64 lowercase hex digits>",
  "status": "passed",
  "passed": 10,
  "total": 10
}
```

The input digest covers the **exact UTF-8 bytes of submitted `job.text`**, not
JSON reserialization or guest stdout. The definition digest must match the
operator's pin. Counters are required integers: `1 <= total <= 1000000`,
`0 <= passed <= total`; pass requires equality, failure requires inequality.
Unknown receipt fields, duplicate keys (anywhere in the result), changed IDs,
wrong digests and prose pretending to be JSON are rejected. Other top-level
adapter result fields remain adapter-owned untrusted evidence.

The receipt must be constructed by the trusted adapter after actual checks,
never forwarded from the model or submitted program. Adapter-specific behavior
must distinguish checked failures from missing/ambiguous execution evidence.
Passing finite operator-owned tests still does not mean general correctness.

## Verification scope

Core tests exercise approval binding, config pins, strict receipts, exact-byte
binding, whole-plan preflight, early stop, bounded continuation, exhausted
failure and inconclusive stops. The external-binary transport fixture tests the
CLI but does not claim real inference or real sandbox results. Those require
separate adapter/runtime end-to-end evidence.
