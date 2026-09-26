# Automation

ContextBridge provides three different automation layers. Choose the smallest
one that represents the work.

## Durable schedules

A schedule stores one job, timing, bounded history, pause/resume state, and
optional sequential follow-ups. Example one-shot local job:

```json
{
  "name": "Release note draft",
  "job": {
    "route": "default",
    "provider": "ollama",
    "prompt": "Draft a concise release note from the submitted change list.",
    "text": "Changed: ...",
    "output": { "mode": "text" }
  },
  "timing": {
    "type": "at",
    "at": "2026-09-21T09:00:00Z"
  },
  "enabled": true
}
```

Add and manage it:

```sh
contextbridge schedule add --config ./config.yml --file ./schedule.json
contextbridge schedule list --config ./config.yml
contextbridge schedule show --config ./config.yml SCHEDULE_ID
contextbridge schedule pause --config ./config.yml SCHEDULE_ID
contextbridge schedule resume --config ./config.yml SCHEDULE_ID
contextbridge schedule run --config ./config.yml SCHEDULE_ID
contextbridge schedule delete --config ./config.yml SCHEDULE_ID
```

Timing types are `at`, `interval`, `daily`, `weekdays`, `weekly`, and five-field
`cron`, with an explicit IANA timezone where wall-clock meaning matters.
Intervals are elapsed-time schedules. Missing daylight-saving times are
skipped; repeated local times may run twice.

Claims are persisted before dispatch. On service restart, an occurrence that
was running is marked interrupted and is not automatically replayed because an
external engine may already have accepted it. Recurring missed occurrences are
coalesced instead of creating an unbounded backlog.

Follow-up steps run only after the previous step has a saved successful result.
Templates may reference previous text, JSON, or artifact names. The referenced
bytes remain untrusted submitted content. A verified prior image can be handed
to a compatible local or adapter step only after size, media, and SHA-256 are
rechecked; URL-only or cross-run files are not guessed or fetched silently.

## Deterministic pipelines

Use a configured pipeline when the steps, providers, and transformations are
known in advance. Pipeline definitions are operator configuration, have fixed
step/runtime ceilings, and use the same execution policy and worker contracts
as single jobs. Prefer this over an agent for stable production workflows.

Existing pipelines are `linear` by default and retain declaration-order
execution. ContextBridge also validates the bounded `mode: dag` contract at
configuration load, including dependencies, templates, cycles, fan-in/out and
deterministic topological order. DAG execution is deliberately not enabled in
this release; the run endpoint returns a conflict before creating a run or
child job. See [dag-pipelines.md](dag-pipelines.md).

## Bounded agents

Use an agent when the model must propose the text-step decomposition. There are
three authority levels: local-only automatic, named operator policy, and exact
hash-reviewed one-off plan. See [bounded-agent.md](bounded-agent.md).

No automation layer turns generated text into shell code or silently expands
its own provider, cost, egress, retry, tenant, or tool authority.
