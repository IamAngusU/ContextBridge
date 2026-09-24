# Authoritative execution events

ContextBridge exposes a versioned, bounded per-job lifecycle history:

```text
GET /v1/cluster/jobs/JOB_ID/events?after=SEQUENCE&limit=100
```

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

`source=relay` and `authority=authoritative` mean CB owns and persisted that
orchestration transition. They do not mean that CB can prove arbitrary claims
inside a model, runtime or tool.

The most recent 256 execution events per job are retained while the job is
retained. `gap=true` tells a reconnecting client that its cursor predates the
oldest retained event. The terminal Job record and validated result remain the
ultimate source of truth.

Worker `Job.Progress` is an advisory observation. Its sequence, phase, percent
and busy bit are persisted as `execution.progress`, but its text and detail are
never copied into the event plane. At most 64 progress observations are kept
per job, leaving ample room for authoritative lifecycle evidence. A worker
reporting `100%` or `done` cannot complete a job. Plaintext progress remains
disabled for encrypted jobs; no E2EE payload is exposed through this endpoint.

The global `/v1/cluster/events` feed remains an operator-facing history and is
not a substitute for this atomically committed per-job contract.
