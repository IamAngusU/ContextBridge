# Management API

ContextBridge exposes two deliberately separate management surfaces:

- the relay API for a custom backend, dashboard, or operator tool; and
- a loopback-only local configuration API for the machine that owns the
  configuration file.

This separation keeps a remote observer credential from becoming a file-write
credential. A browser should authenticate to your own backend. The backend
keeps its ContextBridge bearer token out of JavaScript and calls the relay API.

## Start a read-only UI integration

Create a private observer credential without printing the bearer value:

```sh
contextbridge integrate ui \
  --subject operations-ui \
  --allowed-subjects website-api,batch-worker \
  --allowed-tenants production \
  --lifetime-hours 720 \
  --write-env ./contextbridge-ui.env
```

`--allowed-subjects` and `--allowed-tenants` are credential-bound filters. If
both are supplied, a job or pipeline run must match both. Empty filters retain
the cluster-wide observer behavior for backwards compatibility. A scoped
observer uses exact, case-sensitive owner and tenant matches; similarly named
namespaces cannot widen each other's visibility. It cannot read the global
event feed because global events do not always
carry enough ownership evidence to filter safely; use the per-job and
per-pipeline event endpoints instead.

The backend can discover its effective identity and limits with:

```text
GET /v1/cluster/whoami
```

It can discover the authenticated OpenAPI 3.1 contract with:

```text
GET /v1/cluster/openapi.json
```

Both endpoints accept admin, observer, producer, and node credentials and
never return a bearer value or stored credential hash.

## Page, filter, and stream execution history

The original bounded array response remains available for existing clients:

```text
GET /v1/cluster/jobs?limit=50&status=running
```

New clients should opt into the cursor response:

```text
GET /v1/cluster/jobs?page=1&limit=50&status=running&tenant_id=production
```

The response schema is `contextbridge.job-history-page.v1` and contains
`jobs`, `has_more`, `next_cursor`, and `scanned`. Pass `next_cursor` unchanged
on the next request. Cursors are bound to the credential and filters, so they
cannot be reused to widen visibility. Each request scans a bounded number of
metadata-only history records; request and result payloads are not repeated in
the list response.

Live job and pipeline timelines use the authenticated SSE endpoints:

```text
GET /v1/cluster/jobs/JOB_ID/events/stream?after=SEQUENCE
GET /v1/cluster/pipeline-runs/RUN_ID/events/stream?after=SEQUENCE
```

They replay durable authoritative events, report retention gaps, send bounded
heartbeats, close periodically, and resume from `Last-Event-ID` or `after`.
Visibility is checked before streaming and every credential has bounded stream
capacity. There is intentionally no unscoped global SSE feed.

The dependency-free reference client at
[`examples/server-app/contextbridge-ui-client.mjs`](../examples/server-app/contextbridge-ui-client.mjs)
provides `whoami()`, `openAPI()`, `jobPage()`, `jobEventStream()`, and
`pipelineEventStream()`. Its stream helpers enforce content type and contract
headers, bound individual SSE frames, parse UTF-8 and JSON strictly, resume
with a cursor, and stop automatically on terminal events.

## Edit the local configuration safely

The local service exposes these endpoints only to a loopback peer with the
local service bearer token:

```text
GET  /v1/config/schema   describe fields, enums, ranges, and editing semantics
GET  /v1/config          read redacted editable YAML and its revision
POST /v1/config          validate a proposed YAML document without writing
PUT  /v1/config          validate and atomically apply a proposed document
```

The schema response contains machine-readable `constraints` with paths,
types, enums, integer/number limits, list sizes, formats, secret/write-only
markers, and references to policy-controlled maxima. A UI can create selects,
sliders, numeric inputs, and pre-submit messages from this catalog. The server
remains authoritative: POST and PUT run the complete startup validator,
including cross-field dependencies that a single input control cannot express.

The basic client flow is:

1. `GET /v1/config`.
2. Let the user edit the returned `yaml` value.
3. Keep every unchanged `__CONTEXTBRIDGE_KEEP_SECRET__` marker intact.
4. `POST /v1/config` with `yaml` and the returned `revision` as
   `base_revision`.
5. After user confirmation, send the same body with `PUT /v1/config`.
6. If the response says `restart_required: true`, restart through the service
   manager or normal ContextBridge lifecycle.

The API never returns inline secrets or environment references. The keep
marker restores the exact current scalar only while applying the proposal. A
marker for a secret that does not already exist is rejected. A UI may submit a
new secret, but it will be redacted on the next read.

Every proposal uses the same strict parser, known-field check, defaults,
secret-file checks, cross-field validation, finite-number checks, numeric
bounds, and URL/path policy as startup. Multiple YAML documents and aliases
are rejected on this editing surface. `base_revision` is mandatory; stale
writes return `409 config.revision_conflict`. Successful changes are written
through a private temporary file, synced, and atomically replace the regular
non-symlink config file.

Apply does not pretend to hot-reload settings that are owned by running
listeners, stores, workers, or runtimes. It reports the changed top-level
sections and makes restart semantics explicit.

Do not publish or reverse-proxy `/v1/config` from a local service. A reverse
proxy on the same machine makes its upstream connection look loopback-local.
Hosted deployments should keep this endpoint private to the per-customer
instance and expose a separate account-aware control plane instead.

## Error contract and request correlation

Relay JSON errors use `contextbridge.error.v1` with stable `code`, `message`,
and the backwards-compatible `error` field. Every relay response also carries
an `X-Request-ID` header for logs and support. Point reads intentionally return
the same not-found body for a missing resource and an out-of-scope resource.

The relay does not emit permissive browser CORS headers. This is intentional:
put credentials in a backend-for-frontend or trusted desktop process, not in a
public browser bundle.
