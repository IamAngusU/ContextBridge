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

## Live snapshots

The same GET projection can show the latest **partial** snapshot while the job
is running (`evidence_phase: progress`, `progress_sequence`, `attempt`). After
termination, `evidence_phase: final` uses only the terminal result. Missing final
evidence is not silently replaced with a partial progress inventory. A restart
that marks execution ambiguous must not revive it or replay side effects.

```http
GET /v1/cluster/jobs/JOB_ID/activity/stream
Authorization: Bearer YOUR_EXISTING_JOB_READER_CREDENTIAL
```

The separate SSE stream emits `event: activity.snapshot` and a full
`JobResourceActivity` as `data`. **Replace**, do not append, the previous snapshot.
Unchanged snapshots are suppressed; normal progress sequence changes can produce
another snapshot with the same resources. There are no SSE IDs or replay log:
`after` and `Last-Event-ID` are rejected. Reconnect for the current state. A fast
job may only be observed after completion; a snapshot is not a guarantee that
every intermediate action will be delivered.

Streams close on terminal state, absent opt-in or E2EE, client disconnect,
permission loss, or after 30 seconds. Clients reconnect while still interested
and authorized; clients with Bearer credentials can use an authenticated fetch
stream. Do not put secrets in URLs. Token expiry/revocation and current job scope
are checked before each snapshot (250 ms polling). Capacity is shared with
lifecycle SSE: 64 total and 8 per role/subject. Rejected connections return 503
and `Retry-After: 1`. Writes are bounded to 10 seconds; heartbeats every 10 seconds.
The stream is `no-store, no-transform`, with proxy buffering disabled.

Discovery: the relay advertises `live_resource_activity_snapshots_v1`. The
executing local Core separately advertises `resource_activity_progress_v1` in
scoped `GET /v2/adapter/status.features`. Only send the optional `activity`
progress field if that feature is present **and** `job.output.activity` is true.
Its manifest has the same schema and limits as final receipts below. Older
Core versions can reject unknown progress fields; lack of the feature means
omit the field, not retry a rejected mutation.

Each progress manifest is the adapter's complete current bounded snapshot,
not a delta. A higher sequence replaces it; an omitted manifest preserves it;
an explicit empty `items` array clears it. Invalid reports clear prior evidence
and carry `activity_status: invalid` across the worker, without failing the job.
Duplicate/stale sequence numbers and foreign node/attempt/lease credentials
cannot replace current evidence. Core revalidates before storing and projecting.
The existing worker polls local progress every 500 ms. The relay, worker and
local Core all need this version for end-to-end delivery.

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

- Activity shares the job progress/result's existing retention and job-read ACL. It is
  not a new global memory or second audit database. Labels/URLs can be sensitive;
  enabling it intentionally publishes them to those job readers.
- The relay revalidates before projecting. Sealed input or result jobs return
  `encrypted`, never a parallel plaintext resource list.
- Job histories and lifecycle SSE remain content-minimized. Semantic resource
  snapshots are available only through the job's scoped detail/activity surfaces;
  no historical list of snapshots or global resource audit log is created.
- Existing lifecycle remains available even if an adapter is absent or does not
  report resources. No private provider is required.
- Pipeline activity uses persisted parent/child edges. Agent steps are not
  relabeled as subagents. Durable agent-run grouping, nested subagents,
  schedule cards and authenticated thumbnail delivery remain separate work.
- The optional workspace adapter is the first concrete producer: tool actions,
  file inventory/content reads, direct change-set creation/update, ZIP
  export/reuse. It does not claim to view images, browse websites, or list every
  internally touched plan/transaction file. Its progress report follows each
  tool operation, not every internal file access; fast jobs may complete before
  the next worker poll. No artificial runtime wait is added for visualization.

No UI is bundled; terminal, web and external clients can consume the same API.
