# Bounded agents and operator authority

ContextBridge separates three authorization tiers. The planner proposes text
steps; it never grants itself a provider, credential, route, budget, tenant,
worker group, tool, file operation, or retry.

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

## Execution binding

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

## Adapter evidence and external changes

A bounded plan may use an explicitly allowlisted adapter profile. For adapters
with a machine-shaped request, the operator can place a non-secret
`agent_instruction_contract` on that profile. ContextBridge gives the planner
only that syntax description, never the profile's credential paths or other
options. The adapter remains responsible for validating the exact request and
its own least-privilege boundary. Core carries the exact adapter request in
submitted content rather than its trusted prompt wrapper. Adapter steps cannot
also consume a previous result; a following model step can consume their
normalized evidence instead.
Contracted adapter evidence must be valid, unambiguous JSON; Core validates and
compacts it before it can become the next step's submitted text.

Structured adapter evidence is carried to a later step as normalized,
explicitly untrusted text. It is not promoted into the later model's system
instructions. This supports workflows such as research, source-aware drafting,
and verification while keeping the plan limited to the reviewed providers and
profiles.

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
