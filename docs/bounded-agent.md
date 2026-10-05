# Bounded agents and operator authority

ContextBridge separates three authorization tiers. The planner proposes text
steps; it never grants itself a provider, credential, route, budget, tenant,
worker group, tool, file operation, or retry.

An optional [`--mode lazy` work style](lazy-mode.md) encourages reuse and minimal
correct changes. It is included in plan approval and does not change any of the
authority tiers below.

## 1. Local-only automatic work

For a low-risk local text workflow, the operator can allow up to three Ollama
steps without approving a plan file:

```sh
contextbridge cluster agent auto \
  --config ./config.yml \
  --goal "Draft a concise release note and check it for unsupported claims"
```

This tier fixes egress to `local_only`, permits only Ollama, uses one attempt
per job, and grants no artifact, file, shell, code-execution, remote API, or
adapter authority. A local model without monetary evidence remains cost
unknown; it is never rewritten as `$0`.

## 2. Named project authority

An operator can define an enabled envelope once and reuse it for a project.
The envelope controls the planner, allowed execution providers, tenant/group,
egress, known or unknown cost handling, maximum steps, per-step timeout, and
total runtime.

```yaml
cluster:
  policies:
    agent_authorities:
      release-copy:
        enabled: true
        tenant_id: docs
        group: workstation
        planner:
          provider: ollama
          model: qwen3:8b
          timeout_seconds: 180
        allowed_providers: [ollama]
        egress: local_only
        allow_unknown_cost: true
        max_steps: 3
        step_timeout_seconds: 180
        max_runtime_seconds: 600
```

Run it without a per-run approval:

```sh
contextbridge cluster agent auto \
  --config ./config.yml \
  --policy release-copy \
  --goal "Draft a release note, then check every factual claim"
```

Command-line flags cannot widen a named envelope. Edit the operator-owned
configuration to change authority. Disabling or narrowing the policy causes
later runs to stop or require a newly matching plan.

### Preview a named policy before doing the work

The same envelope can be used without immediately executing its proposed steps:

```sh
contextbridge cluster agent plan --config ./config.yml --policy release-copy --goal "Draft a release note, then check every factual claim" --out ./release-plan.json
contextbridge cluster agent run --config ./config.yml --plan ./release-plan.json --approve sha256:REVIEWED_HASH --ask critical
```

`plan --policy` inherits the exact configured planner, tenant, worker group,
providers/profiles, egress and budgets. It writes a `manual_hash` plan, not an
automatically executable plan. `run` requires the exact reviewed hash and checks
that both the execution binding and the named authority still match. Even a
re-hashed edited plan cannot claim a different envelope under the policy's name.
As with `auto --policy`, planner/allowlist/limit override flags are rejected.

Preview means **no proposed work steps**, not no computation: one ordinary
planner job runs and may incur cost or network use as authorized by the policy.
An adapter selected as the planner still runs that adapter. Route previews are
read-only snapshots, not reservations or guarantees of eventual availability.
Choose a local model planner if preview itself must be local model-only work.

For an adapter, local/remote classification applies to the exact profile via
`cluster.policies.execution.adapter_profile_classifications`. Named authorities
check the planner and **every** allowed profile before planning; a local first
profile cannot authorize a remote sibling. Unclassified profiles retain the
provider's conservative classification. This changes neither relay/tenant
authorization nor the `--ask critical` confirmation gate for adapters.

### Optional local reasoning

For an explicitly selected thinking-capable Ollama model, an operator can set
`ollama_think: true` on that `engines` entry, with an explicit model and positive
`max_output_tokens`. Default remains `false`; prompts and jobs cannot toggle
this setting. Use a separate engine/route when other tasks should remain fast.
Check the model's support first: [Ollama thinking controls](https://docs.ollama.com/capabilities/thinking).
Only the final `response` is consumed. The private `thinking` field is neither
returned nor forwarded to later tools. Token/time/output limits still apply;
thinking-only or truncated output is not a successful result, and there is no
silent retry with a different mode. Changing this setting changes the execution
binding and invalidates earlier approvals. This is an opt-in mechanism, not a
quality or latency guarantee for any particular model.

## 3. Reviewed one-off plans

Broader one-off work uses a separate plan and exact hash approval:

```sh
contextbridge cluster agent plan \
  --config ./config.yml \
  --goal "Draft one concise release note and verify it" \
  --planner-provider ollama \
  --allow-providers ollama \
  --max-steps 2 \
  --out ./reviewed-plan.json

contextbridge cluster agent run \
  --config ./config.yml \
  --plan ./reviewed-plan.json \
  --approve sha256:REVIEWED_HASH
```

Planning is an ordinary attributable job. The decoded proposal rejects unknown
fields, unsafe or duplicate step IDs, targets outside the local allowlist,
oversized text, and more than six steps.

Fresh planner proposals may express a named adapter's `instruction` as either
the existing JSON string or a JSON object. Core strictly validates and compacts
an object into the existing string representation before policy validation and
hash approval; it does not invent fields, repair actions, round numeric IDs, or
grant a profile. Non-adapter instructions remain text-only. Saved approved plans
remain string-only, so reading an old plan never silently changes its meaning.

After structural validation, Core checks every proposed target's local route and
every dynamic model-to-adapter handoff's nonempty operator contract. This
preflight runs again before the first execution step. A broken later route or
missing handoff contract therefore stops before any earlier work step is
submitted. It does not validate an adapter-specific action, prove the goal will
be achieved, or predict generated JSON; runtime validation, lease fencing and
per-step confirmation remain necessary. Completed work is not rolled back when
a later runtime/provider error occurs.

## Execution binding

### Optional execution confirmations

`agent run` and `agent auto` accept `--ask all|critical|none` independently of
authorization. `all` asks before every execution step, showing the exact resolved
prompt/content and a request-bound confirmation code, including generated adapter
JSON. `critical` asks before every adapter step and every remote or unclassified
provider; only classified local model-only steps skip the question. Adapter
action names such as `read` are not trusted as proof of safety, so this conservative
mode currently also asks for file creation and inspection. `none` preserves the
existing behavior after hash approval or named-policy authorization. It is not
permission to escape the configured workspace, enable shell execution, publish,
change policy, or bypass adapter checks.

```sh
contextbridge cluster agent run --plan ./reviewed-plan.json --approve sha256:REVIEWED_HASH --ask critical
```

EOF, a wrong answer, cancellation or timeout stops before submitting that step.
Earlier completed steps remain completed; declining does not roll them back.
Waiting for confirmation consumes the existing run/step deadline. The planner
request itself is still authorized by the explicit planning command, not this
additional execution gate. For unattended work choose `none` with an appropriately
narrow operator policy. The default remains `none` for compatibility with existing
hash-approved and policy-authorized workflows.

## Execution binding details

Every accepted plan records:

- a SHA-256 of the effective secret-free configuration;
- a versioned execution fingerprint covering routes, providers, engines,
  models, adapter profiles, portable resources, cluster execution policy, RAG
  configuration, and relay identity;
- component digests that explain which execution category changed;
- the relay URL, planner job, node, provider, model, and cost evidence.

`agent run` recomputes the binding immediately before execution. If the
effective configuration or relay changed, it refuses the old approval. The
full-config digest is intentionally conservative during the alpha: an
irrelevant operational change may require review, but an execution-relevant
change cannot inherit stale authority.

## Hard boundaries

- Plans are text-only and contain one to six steps.
- Automatic local plans contain at most three steps.
- Every step uses `max_attempts: 1`.
- A named authority's `max_cost_usd` is one aggregate reservation budget for
  the planner and all cost-bounded remote steps. Each completed reservation is
  subtracted permanently before the next job is submitted; it is not a
  reusable per-job allowance. Unknown-cost remote targets still require the
  separate explicit `allow_unknown_cost` authority.
- Prior output is carried as untrusted submitted content, not promoted into
  trusted instructions.
- There is no arbitrary shell, host filesystem, URL-fetch, plugin, or tool
  loop. An explicitly selected adapter contract may expose bounded
  workspace-relative file or archive operations; the profile, scoped
  credential, adapter validator, lease and operator policy remain independent
  gates.
- Waiting interrupted after submission is ambiguous: inspect the recorded job
  before deciding whether to retry.
- Known costs remain subject to execution policy and reservations; unknown
  cost requires an explicit operator decision.

The model proposes work inside the envelope. The operator owns the envelope.

### CLI regression proof

The standard Go suite checks named previews, exact approval, scope propagation,
policy changes, CLI override rejection and preflight failures. For an isolated
external-binary proof, build `./cmd/contextbridge`, set
`CONTEXTBRIDGE_TEST_BINARY` to that executable's absolute path, and run:

```sh
go test ./cmd/contextbridge -run TestAgentPolicyPreviewExternalBinary -count=1 -v
```

This test invokes the real CLI against a disposable loopback relay fixture. It
checks that preview submits one planner job and zero work jobs, wrong approval
submits no work, and exact approval submits one scoped step. The fixture supplies
deterministic results: this is a CLI/transport proof, not a model-quality or real
worker/adapter proof. The test is skipped unless the binary is explicitly given.

## Adapter evidence and external changes

A bounded plan may use an explicitly allowlisted adapter profile. For adapters
with a machine-shaped request, the operator can place a non-secret
`agent_instruction_contract` on that profile. ContextBridge gives the planner
only that syntax description, never the profile's credential paths or other
options. The adapter remains responsible for validating the exact request and
its own least-privilege boundary. Core carries the exact adapter request in
submitted content rather than its trusted prompt wrapper. Static adapter steps
do not append previous results; the explicit `contextbridge.previous-json.v1`
handoff described below is the only dynamic request path. A following model
step can consume normalized adapter evidence instead.
Every fresh proposed step must explicitly include boolean `use_previous`.
Missing/null values are rejected before any execution. A step with `false`
receives no previous result unless it explicitly selects `input_steps` (below);
use `true` for model steps that need only the immediately preceding result.
Core does not guess data dependencies from natural language. Existing approved
plan encoding is unchanged, and adding an omitted flag is never an automatic
repair of a hash-approved plan.

Contracted adapter evidence must be valid, unambiguous JSON; Core validates and
compacts it before it can become the next step's submitted text.

Structured adapter evidence is carried to a later step as normalized,
explicitly untrusted text. It is not promoted into the later model's system
instructions. This supports workflows such as research, source-aware drafting,
and verification while keeping the plan limited to the reviewed providers and
profiles.

### Select several earlier results without losing task context

A non-adapter step may set `input_steps` to an ordered list of earlier step IDs
with `use_previous: false`. For example, after authorized steps named `memory`,
`inspect` and `checks`, a model step can use:

```json
{
  "id": "repair",
  "provider": "ollama",
  "instruction": "Use the project constraints, current code and actual test diagnostics to propose a correction. Cite source step IDs. Do not claim that a proposal has passed tests.",
  "use_previous": false,
  "input_steps": ["memory", "inspect", "checks"]
}
```

This is a step fragment, not an executable whole plan. The earlier steps must
exist and use explicitly allowed tools/profiles. A model's statement that tests
passed is not a test receipt. The planner now knows this data-flow option;
Core does not guess dependencies or insert read/write actions on its behalf.

The selected outputs and original goal become one strict JSON submitted-content
bundle (`contextbridge.agent-evidence.v1`). Each input carries its step ID,
provider/profile, job ID, assigned node, SHA-256 and complete content. Metadata
describes observed execution, not factual correctness or an authenticated
receipt. Both the goal and all returned evidence remain untrusted data, never
system instructions. No unselected result or implicit predecessor is added.

- Selection/order is visible in preview and bound to the plan approval hash.
- Only unique **earlier** IDs in the same plan are allowed; no self/forward or
  cross-run references. At most five earlier results can fit a six-step plan.
- `input_steps` cannot be mixed with `use_previous: true` or used on an adapter
  step. Exact model-to-adapter JSON handoffs remain unchanged.
- The complete encoded bundle, including the goal and metadata, is limited to
  128 KiB. Missing, corrupted or over-limit evidence stops before submitting the
  dependent step; nothing is silently shortened or replaced with a summary.
  The ordinary 256 KiB source-output limit and rejection of truncated provider
  results still apply. Already completed work is not rolled back.
  This byte ceiling is not a tokenizer/context-window guarantee: choose narrow
  tool outputs appropriate to the selected model. Automatic model-aware
  compaction and protection against a provider's own context clipping are not
  supplied by this feature.
- Only explicitly referenced outputs are retained in this run's private memory;
  nothing is persisted by this mechanism or shared across users/runs. Existing
  tenant/group, destination, egress, cost, time, confirmation and one-attempt
  restrictions still apply to the resolved step. This feature does not redact
  secrets from an authorized source: use appropriately scoped read tools.
- Omitting the field preserves legacy behavior and old plan hashes. Empty lists
  have no effect. This does not change `do` into an automatic tool loop.

An allowed workspace adapter's task-memory read can supply `memory`, while a
separate inspection or test action supplies other inputs. Persistent storage,
compaction, pin/todo rules, access control and writes remain the adapter's job.
Automatic memory loading/saving, durable resume and validated recipe promotion
are **not** implemented by `input_steps`. Optional conditional stop/repair is a
separate [operator-pinned verification gate](agent-verification.md), not inferred
from model prose or test-looking text.

Run the CLI transport proof with an explicitly built binary:

```sh
CONTEXTBRIDGE_TEST_BINARY=/absolute/path/contextbridge go test ./cmd/contextbridge -run TestAgentEvidenceExternalBinary -count=1 -v
```

On PowerShell, set `$env:CONTEXTBRIDGE_TEST_BINARY` first. The test uses the real
CLI and a disposable loopback relay fixture, checks hash-bound selection and
scope propagation, and supplies deterministic provider results. It does not
measure a real model's reasoning or prove an installed persistent-memory adapter.

### Machine-readable model results

A non-adapter step can also set `output_mode: "json"` to request the bridge's
actual JSON output contract, including for a final answer. Prompting a model to
"return JSON" without this field still uses the default text output contract.
Valid values are `text` and `json`; omission preserves the legacy default. The
choice is shown in preview, approval-hash-bound and cannot grant file artifacts
or new tools. Adapter output remains governed by its own contract; the field is
not allowed on adapter steps. Explicit `text` cannot precede an exact adapter
JSON handoff, which requires JSON.

For an explicitly JSON model step, Core rejects text/Markdown envelopes, mixed
text/JSON results, missing JSON and ambiguous JSON before using the result.
This checks structure, not a domain schema or the truth of the answer. A later
tool must still validate all required properties and independently authorize
any side effect. Failure stops the run without an automatic retry or text fallback.

A local workspace adapter may likewise define exact versioned JSON actions for
an owned workspace. That exception does not authorize arbitrary host paths,
shell commands, executable selection, code execution, downloads, deletion or
promotion. Core transports the exact request; the out-of-tree adapter must
validate paths and action shape and must claim a fenced mutation lease before
the first write. The default Ollama-only automatic tier has no adapter or file
authority.

A reviewed plan or named operator authority may use the
literal `contextbridge.previous-json.v1` handoff after a non-adapter step. Core
then accepts only one strict JSON object as the next adapter request. This makes
read-model-write workflows possible without turning untrusted prose into a
shell or concatenating it with a trusted instruction.

External mutation is a different authority tier. Agent steps cannot submit the
reserved `scheduled_action` task or turn evidence into a write credential. A
channel must stage a typed payload and opaque destination, obtain a relay
preview, and explicitly confirm the scheduled action under a credential-bound
policy. The one-attempt action adapter then claims its fenced lease immediately
before the provider call. This is how posting or updating can be offered
without making a prompt an administrator.
