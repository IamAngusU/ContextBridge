# Authoritative execution events

ContextBridge exposes a versioned, bounded per-job lifecycle history:

```text
GET /v1/cluster/jobs/JOB_ID/events?after=SEQUENCE&limit=100
```

Pipeline runs expose the same cursor contract, including stable step and child
job identities:

```text
GET /v1/cluster/pipeline-runs/RUN_ID/events?after=SEQUENCE&limit=100
```

Applications that need live delivery can watch the same durable records over
bounded server-sent events (SSE):

```text
GET /v1/cluster/jobs/JOB_ID/events/stream?after=SEQUENCE
GET /v1/cluster/pipeline-runs/RUN_ID/events/stream?after=SEQUENCE
Accept: text/event-stream
```

Each execution event uses its durable sequence as the SSE `id` and its stable
event type as the SSE `event`. Reconnect with either `after=N` or the standard
`Last-Event-ID: N` header; an explicit query cursor takes precedence. The
stream closes after a terminal lifecycle event or after a bounded 30-second
watch window, so clients reconnect instead of consuming an immortal relay
goroutine. It emits keepalive comments, never synthetic progress.

If retention removed events before the requested cursor, the response sets
`X-ContextBridge-Event-Gap: true` and emits one `contextbridge.gap` transport
control record before replaying the oldest retained execution event. That
control record is not an execution event and has no event sequence. Clients
must use the terminal Job or PipelineRun record as the ultimate truth after a
gap. At most 64 event watches are live per relay and at most eight per token
subject; excess requests receive `503` plus `Retry-After` rather than letting
one credential or many slow clients create unbounded memory or goroutines.

SSE is only an additive delivery surface. It applies the same bearer-token,
role and producer-ownership checks as bounded polling, contains the same
content-minimizing event JSON, and does not stream incremental result content
(which remains a separate contract).

The endpoint accepts administrator and observer credentials. A producer may
read only events for its own job. Responses use `contextbridge.event.v1` and a
relay-assigned monotonic sequence per job:

```json
{
  "events": [
    {
      "schema": "contextbridge.event.v1",
      "job_id": "job_...",
      "seq": 3,
      "type": "worker.assigned",
      "source": "relay",
      "authority": "authoritative",
      "ts": "2026-09-24T20:00:00Z",
      "attempt": 1,
      "node_id": "node_..."
    }
  ],
  "after": 2,
  "next": 3,
  "oldest_retained": 1,
  "newest": 3,
  "gap": false
}
```

The CLI exposes the same cursor without requiring application code:

```bash
# One bounded page for people.
contextbridge cluster events JOB_ID --after 0

# The exact versioned page for programs.
contextbridge cluster events JOB_ID --after 0 --limit 100 --json

# Continue from the cursor until CB commits a terminal lifecycle event.
contextbridge cluster events JOB_ID --after 12 --follow

# Follow the durable lifecycle of a whole pipeline and all of its steps.
contextbridge cluster events RUN_ID --pipeline --follow
```

`--follow` is bounded polling, not an invented streaming claim. Each request
uses the last durable `next` cursor, so a transient disconnect cannot turn an
event into a second execution. JSON follow mode emits one complete page per
line. A producer, observer or administrator token may be selected with
`--token`; normal configuration token resolution remains the default.

Authoritative lifecycle events are written in the same Bolt transaction as
the corresponding durable job transition. If either write fails, neither
state nor event commits. Current event types are:

- `job.accepted`
- `job.queued`
- `route.selected`
- `worker.assigned`
- `execution.started`
- `job.retrying`
- `job.completed`
- `job.failed`
- `job.cancelled`
- `job.ambiguous`
- `execution.progress` (`authority=advisory`, `source=worker`)

Pipeline streams use these relay-authored authoritative types:

- `pipeline.started`
- `pipeline.step.queued`
- `pipeline.step.started`
- `pipeline.step.completed`
- `pipeline.step.failed`
- `pipeline.step.cancelled`
- `pipeline.step.ambiguous`
- `pipeline.completed`
- `pipeline.failed`
- `pipeline.cancelled`

Every step event carries the durable pipeline `run_id`, configured `step_id`
and actual child `job_id`. The event is committed in the same transaction as
the corresponding run or child-job state. This lets an application reconnect
without inventing progress from presentation logs or losing the relationship
between a pipeline step and the job that executed it.

`source=relay` and `authority=authoritative` mean CB owns and persisted that
orchestration transition. They do not mean that CB can prove arbitrary claims
inside a model, runtime or tool.

The most recent 256 execution events per job or pipeline run are retained while
that record is retained. Pipeline-event storage is removed with its terminal
run by the same retention transaction. `gap=true` tells a reconnecting client
that its cursor predates the oldest retained event. The terminal Job or
PipelineRun record and validated results remain the ultimate source of truth.

Worker `Job.Progress` is an advisory observation. Its sequence, phase, percent
and busy bit are persisted as `execution.progress`, but its text and detail are
never copied into the event plane. At most 64 progress observations are kept
per job, leaving ample room for authoritative lifecycle evidence. A worker
reporting `100%` or `done` cannot complete a job. Plaintext progress remains
disabled for encrypted jobs; no E2EE payload is exposed through this endpoint.

The global `/v1/cluster/events` feed remains an operator-facing history and is
not a substitute for this atomically committed per-job contract.
