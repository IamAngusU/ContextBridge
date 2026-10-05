# Opt-in resource activity

This provider-neutral contract lets an out-of-tree adapter report what it
actually used or produced. It does not parse an LLM's prose into tool receipts,
expose hidden reasoning, or depend on a private adapter, model or filesystem.

## Request and retrieve

Set `output.activity: true` in a job payload. Defaults remain off. For bounded
agent plans, the operator can use:

```console
cb cluster agent plan --policy PROJECT_POLICY --goal "Inspect the project" --activity --out plan.json
```

The same switch is supported by `cluster agent auto`. The saved plan's optional
`report_activity` field is covered by its approval digest. The planner cannot
enable it. Existing plans omit it and retain their old hashes. Execution prints
each work job's activity endpoint; this is not a durable agent-run tree.
The relay and executing worker/local Core must support this contract as well;
updating a CLI alone does not upgrade an already running pool or older adapters.

```http
GET /v1/cluster/jobs/JOB_ID/activity
Authorization: Bearer YOUR_EXISTING_JOB_READER_CREDENTIAL
```

Visibility is identical to reading that job: exact producer owner and tenant
scopes, scoped observer restrictions, or an authorized administrator. Foreign
and absent jobs both return 404. Responses are `Cache-Control: no-store`.
Discovery: `/v1/cluster/protocol` advertises `opt_in_resource_activity_v1`;
`/v1/cluster/openapi.json` describes the typed `JobResourceActivity` response.

Example response fields (ordinary job timestamps omitted here):

```json
{
  "schema": "contextbridge.job-activity.v1",
  "job_id": "job-example",
  "state": "completed",
  "evidence_status": "reported",
  "evidence_source": "adapter_reported",
  "resources": [
    {"id": "a1", "kind": "file", "action": "read", "label": "README.md", "ref": "res_one"},
    {"id": "a2", "kind": "web", "action": "cited", "label": "Documentation", "url": "https://example.test/docs"}
  ],
  "counts": {"file.read": 1, "web.cited": 1}
}
```

Lifecycle `state` comes from the relay, independently of resource evidence.
`evidence_status` is `not_requested`, `pending`, `not_reported`, `invalid`,
`encrypted`, or `reported`. Missing evidence never means "zero tools were used".
Counts describe returned receipts, not necessarily unique files or the complete
process. If `truncated` is true, counts are lower bounds.

## Adapter completion contract

When `job.output.activity` is true, an adapter can add this optional sibling of
`text`/`json` in its existing fenced v2 completion envelope:

```json
{
  "mode": "text",
  "text": "Inspection finished.",
  "activity": {
    "schema": "contextbridge.resource-activity.v1",
    "items": [{"id": "a1", "kind": "tool", "action": "executed", "label": "workspace.inspect"}],
    "truncated": false
  }
}
```

Core accepts at most 32 items and 24 KiB of input **and canonical encoded**
manifest bytes. Strict JSON rejects duplicate/unknown/case-aliased fields.
IDs are unique within the job; IDs/refs match `[a-zA-Z0-9][a-zA-Z0-9_-]{0,95}`.
Labels are plain UTF-8, 1–256 bytes, with no controls or bidi format characters.
Non-web labels cannot contain slash, backslash or colon. Optional SHA-256 is
64 lowercase hex. `ref` is an opaque correlation token, **not a host path,
authorization grant, or download endpoint**.

| Kind | Actions | Meaning |
| --- | --- | --- |
| `tool` | `executed` | The adapter executed this tool action. |
| `file` | `inspected`, `read`, `created`, `updated` | Inventory/metadata is distinct from returned content. |
| `image` | `inspected` | The adapter actually supplied the image to its inspection path; an extension alone is insufficient. |
| `web` | `read`, `cited` | Fetched/consumed source is distinct from a citation or mentioned link. |
| `artifact` | `created`, `reused` | A produced output is distinct from reuse of an existing output. |

Only web entries can carry an optional URL (max 2048 bytes), limited to HTTP(S)
without userinfo, query or fragment. Core never opens it. Adapters must omit
sensitive paths/labels, signed URLs and private query values rather than hide
them in display fields. Clients must escape labels, must not interpret them as
HTML/commands, and must not fetch links, icons or thumbnails automatically.

The record is **adapter-reported**, not an independent proof that a website was
read or an image understood. A buggy or dishonest authorized adapter can report
false evidence. Core validates shape, bounds and scope, not semantic truth.
Model-provider responses cannot publish activity by imitating the envelope.
Invalid optional evidence is dropped with `activity_status: invalid`; it does
not turn an already completed mutation into an error or trigger replay.

## Privacy, lifecycle and current scope

- Activity shares the final result's existing retention and job-read ACL. It is
  not a new global memory or second audit database. Labels/URLs can be sensitive;
  enabling it intentionally publishes them to those job readers.
- The relay revalidates before projecting. Sealed input or result jobs return
  `encrypted`, never a parallel plaintext resource list.
- Job histories and lifecycle SSE remain content-minimized. Follow
  `/jobs/JOB_ID/events/stream`, then fetch activity after completion.
  **Per-resource live streaming is not implemented in this slice.**
- Existing lifecycle remains available even if an adapter is absent or does not
  report resources. No private provider is required.
- Pipeline activity uses persisted parent/child edges. Agent steps are not
  relabeled as subagents. Durable agent-run grouping, nested subagents,
  schedule cards and authenticated thumbnail delivery remain separate work.
- The optional workspace adapter is the first concrete producer: tool actions,
  file inventory/content reads, direct change-set creation/update, ZIP
  export/reuse. It does not claim to view images, browse websites, or list every
  internally touched plan/transaction file.

No UI is bundled; terminal, web and external clients can consume the same API.
