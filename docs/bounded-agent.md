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
- Prior output is carried as untrusted submitted content, not promoted into
  trusted instructions.
- There is no arbitrary shell, filesystem, URL-fetch, plugin, or tool loop.
- Waiting interrupted after submission is ambiguous: inspect the recorded job
  before deciding whether to retry.
- Known costs remain subject to execution policy and reservations; unknown
  cost requires an explicit operator decision.

The model proposes work inside the envelope. The operator owns the envelope.
