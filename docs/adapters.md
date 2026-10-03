<!-- SPDX-License-Identifier: Apache-2.0 -->

# Out-of-tree adapters

The public core exposes a provider-neutral adapter boundary for optional
operator-installed components. No vendor-specific adapter is included here.

An adapter may advertise bounded endpoints with:

- an opaque endpoint ID;
- an operator-defined profile and driver;
- current state and model/capability choices;
- optional fresh-session support;
- an opaque session key;
- a last failure chosen from a bounded vocabulary.

The service normalizes and limits all adapter telemetry. The relay receives
only scheduling evidence needed for placement. Internal endpoint IDs and
session evidence are removed from producer-visible results.

Adapter requirements are hard boundaries: profile, model, reasoning level,
session ownership, and fresh-session capability must be proven by the same
available endpoint. Evidence from multiple endpoints is never combined to make
one endpoint appear capable.

Adapters are separate processes and are not trusted to bypass authentication,
policy, cost, output, or artifact validation.

## Operator setup

Registering a local adapter is explicit and does not install, download, start,
or license third-party software:

```console
contextbridge adapter setup local-speech \
  --driver speech-sidecar \
  --task speech_to_text \
  --route speech_local \
  --model whisper-large-v3-turbo \
  --classification local \
  --option 'language_hints=["de","en"]' \
  --token-file ./secrets/local-speech.token \
  --create-token
```

The command adds one profile, one adapter route, and one least-privilege
principal to the selected configuration. `--create-token` is required before
a missing credential is created. An existing credential is reused without
being displayed or rotated, and conflicting existing configuration is
rejected. The adapter process is still started separately and must prove its
own readiness through the protocol below.

`--classification local|remote` records the reviewed execution boundary for
that exact profile. It does not make an adapter trusted and it does not weaken
per-job egress policy. Repeatable `--option KEY=JSON` values provide bounded,
typed driver settings such as host allowlists or response-size limits. They
are stored in the main configuration and returned to the scoped adapter, so
secret values must remain in independent permission-restricted files and only
their file paths may be referenced by options. Setup output lists option keys,
never option values.

An adapter intended for a bounded agent may expose one non-secret syntax hint
with the reserved option `agent_instruction_contract`. The value is a single
printable string of at most 2 KiB. Only this option is copied into the planner
prompt; paths, credentials, host policy, executable locations, and every other
driver option stay hidden. The hint is not authority: the selected profile,
relay execution policy, scoped credential, adapter-side request validation,
lease, egress and any later confirmation remain independent gates.

For example, a strict research adapter can state that its instruction must be
one versioned JSON request. The planner may then propose that JSON inside an
explicitly allowed adapter step instead of guessing a natural-language
contract. The operator still reviews the resulting plan hash, or separately
opts into a named agent authority.

For an agent adapter step, Core places that exact request in `job.text`; the
ordinary trusted prompt wrapper remains separate. Such a step must set
`use_previous=false`, which prevents prior untrusted evidence from being
silently concatenated into a machine request. Put research or retrieval first,
then pass its normalized result to a later model step for synthesis. Profiles
with an instruction contract are asked for JSON output; Core strictly validates
and compacts that evidence before the handoff.

Use `contextbridge adapter list`, `details`, and `doctor` to inspect the
result. `enable`/`start` and `disable`/`stop` change durable relay admission
for presence-reporting ingress adapters; they do not supervise a local v2
process. A missing or stopped adapter can make only its own routes unavailable.
Ordinary local, pool, and other adapter routes remain independent.

`details`, `enable`/`start`, and `disable`/`stop` accept the stable `adp_...`
UID printed by `list`. While a fresh lease exists they also accept an exact
adapter ID or instance ID, such as `messaging-primary`, when it resolves to one
visible adapter. Ambiguous friendly IDs fail closed and require the stable UID;
the UID also remains the recovery selector after every lease has expired.

Inspection and control use the active named cluster account by default. Select
another account with `--account NAME`, or pass a scoped credential without
putting it into shell history via `--token-file FILE`. The file may contain the
raw `cb_...` credential or a JSON object with a `token` field. Producer
credentials see only adapters in their own scope; durable enable/disable
control still requires an administrator credential.

## External adapter presence

Ingress and control adapters are not compute workers. An independently deployed
component may use its scoped producer credential to renew a bounded
`contextbridge.adapter-presence.v1` lease at
`POST /v1/cluster/adapters/heartbeat`. The reusable request schema is
[`adapter-presence-v1.schema.json`](schemas/adapter-presence-v1.schema.json).

Presence contains only a provider-neutral ID, instance ID, version, state,
bounded capacity counters, error code and descriptive capability IDs. The relay
adds owner and tenant scope from the authenticated credential. Adapter claims
never grant permissions. An expired lease disappears automatically.

Admins can inspect and enable or disable a leased adapter. Disabling is durable
operator intent: the process remains alive long enough to observe the disabled
response and must stop admitting work while continuing its heartbeat. A later
enable does not install software, supply missing secrets or prove readiness;
the adapter must still report a fresh `ready` lease. Producers see only their
own adapters, and scoped observers see only presence within their subject and
tenant scope. When one presence spans several tenants and an observer is
allowed only a subset, CB returns only that tenant intersection and redacts the
non-partitionable readiness, capacity, queue, capability and error telemetry.

This presence contract is independent from the pull-based execution protocol
below. A deployment may implement either, both, or neither without changing
ordinary ContextBridge operation.

An adapter that supports future external side effects may additionally use the
[scoped scheduled action contract](scoped-scheduled-actions.md). Presence is
still only liveness: an administrator-issued producer policy binds the exact
presence UID to one v2 execution profile and principal. At the due time the
relay emits a normal one-attempt v2 job, and the adapter claims its lease
immediately before the side effect. This keeps timers and execution authority
out of channel-specific processes without coupling the public core to a
vendor.

Use two credentials with the same producer subject for a mutating deployment.
The channel keeps the credential containing the scheduled-action policy; the
executor receives a second producer credential without scheduled-action
authority and uses it only for presence. Because the stable presence UID is
derived from cluster, subject, and adapter ID, both still bind to the same UID,
while a compromised executor cannot preview or confirm work for itself.

The execution route must use the exact `scheduled_action` task. ContextBridge
reserves that task against normal job submissions, encrypted reservations and
pipeline steps; only the confirmed scheduler can mint its in-memory admission
capability. The adapter must independently fail closed unless the payload is
the expected `contextbridge.scheduled-adapter-action.v1` opaque-reference
envelope for its profile and exact presence UID.

For that reserved task, the authenticated worker also replaces any caller
claims and supplies `contextbridge_owner_subject` and
`contextbridge_tenant_id` on the leased local job. A mutating adapter must bind
both values to its separately staged destination and payload records. These
execution-only fields are removed from the producer-visible result; ordinary
adapter jobs cannot set them through a cluster payload.

## Local protocol v2

The stable identifier is `contextbridge.adapter.v2`. Adapters connect to the
local service, which binds to loopback by default, using a dedicated scoped
adapter credential. The operator credential (`server.token`) is never accepted
by v2 adapter routes, and an adapter credential cannot access jobs, schedules,
settings, status, OpenAI-compatible ingress, or lifecycle control.

An operator assigns each adapter principal an explicit set of profile IDs:

```yaml
providers:
  adapter:
    auth_mode: scoped
    lease_seconds: 90
    principals:
      local-reviewer:
        token: ${CONTEXTBRIDGE_ADAPTER_TOKEN}
        allowed_profiles: [review-endpoint]
```

`token_file` may be used instead of `token`. Tokens must be independent and at
least 32 characters. Every adapter route in scoped mode must name an
`adapter_profile` covered by at least one principal. The protocol is
pull-based, so the core does not need to launch or supervise an adapter.

| Method | Endpoint | Purpose |
| --- | --- | --- |
| `GET` | `/v2/adapter/status` | Prove v2 support and return the authenticated principal's allowed profile IDs. |
| `GET` | `/v2/adapter/profiles` | Read only the principal's allowed profile labels, drivers, and options. |
| `POST` | `/v2/adapter/heartbeat` | Publish bounded endpoint state and receive short-lived endpoint capabilities. |
| `GET` | `/v2/adapter/jobs/next?profile=ID&endpoint_id=N` | Long-poll with that endpoint capability for one compatible lease; `204` means no work. |
| `GET` | `/v2/adapter/jobs/JOB/lease` | Check whether the supplied lease credentials still own the job. |
| `POST` | `/v2/adapter/jobs/JOB/lease` | Renew an unexpired lease. |
| `GET`, `POST` | `/v2/adapter/jobs/JOB/progress` | Read or publish monotonic, bounded progress for the owned lease. |
| `POST` | `/v2/adapter/jobs/JOB/claim` | Cross the no-automatic-retry boundary. |
| `POST` | `/v2/adapter/jobs/JOB/release` | Return an untouched lease to the queue and invalidate its capability. |
| `POST` | `/v2/adapter/jobs/JOB/complete` | Submit the normalized result and verified artifact bytes. |

Polling includes `X-ContextBridge-Endpoint-Capability`, issued for the exact
authenticated principal, profile, and endpoint ID by the first heartbeat.
Later heartbeats echo that capability in the endpoint's
`endpoint_capability` field. ContextBridge renews the same capability while
the endpoint remains healthy, so a normal heartbeat cannot invalidate an
in-flight long poll; losing the capability requires a fresh registration after
the old grant expires. Endpoint-pinned cluster work also carries the selected
principal identity through relay, worker, and local queue, so another scoped
adapter cannot claim it by reusing the numeric endpoint ID.
Every lease-scoped call includes both
`X-ContextBridge-Lease-Generation: <unsigned-integer>` and
`X-ContextBridge-Lease-Capability: <opaque-value>`. ContextBridge generates a
fresh 256-bit lease capability for every generation and stores only its digest.
The generation is an ABA fence, not an authenticator. A capability cannot be
used by another principal and becomes invalid on release, expiry, completion,
or replacement. Before its first
external side effect, an adapter claims the lease with one generic lifecycle
stage: `prepare`, `mutate`, or `commit`. Once claimed, an ambiguous disconnect
becomes observation-only; ContextBridge will not silently repeat work that may
already have happened.

Heartbeat input is bounded to 128 KiB, 16 endpoints, 50 model choices per
endpoint, and 20 reasoning choices per endpoint. Profile IDs are safe opaque
identifiers. A heartbeat can report only scheduling evidence; it cannot grant
itself policy, credentials, budget, or queue ownership.

The cluster worker observes local adapter progress through an operator-only
endpoint outside `/v1/adapter/*` and `/v2/adapter/*`. Adapter credentials can
neither call nor discover operator state through that path.

## v1 migration

Legacy `/v1/adapter/*` routes used the operator token and did not have opaque
lease capabilities. They are disabled by default. `auth_mode: dual` temporarily
reenables them for an intentional adapter migration and should be treated as a
security downgrade. A v2 adapter may fall back only after an explicit
unsupported-protocol response, never after `401` or `403`. New integrations
must not implement v1.

The contract deliberately contains no driver implementation or vendor
identifier. Out-of-tree and third-party adapters map their own mechanics onto this
small lifecycle without adding implementation-specific code to the core.

The dependency-free [minimal Node.js reference adapter](../examples/adapter-v2/README.md)
implements this public contract without importing ContextBridge internals. Its
unit test uses a fake core to prove request shape, scoped capabilities,
lifecycle ordering, and no retry after an HTTP failure. A second offline CI
test builds the real public binary and proves one complete job across the
process boundary. It returns a fixed string rather than bundling a provider.

## Free adapter conformance v1

An adapter author can run the same public black-box lifecycle checks without a
configured relay or privileged credential:

```sh
contextbridge adapter conformance \
  --adapter node \
  --arg ./examples/adapter-v2/reference-adapter.mjs \
  --profile reference \
  --working-directory . \
  --json > adapter-conformance.json
```

The command starts the exact executable and literal argument list directly,
never through a shell. Each scenario gets a disposable loopback fake core, a
new scoped token in a private temporary file, and the standard
`CONTEXTBRIDGE_URL`, `CONTEXTBRIDGE_ADAPTER_TOKEN`,
`CONTEXTBRIDGE_ADAPTER_TOKEN_FILE`, `CONTEXTBRIDGE_ADAPTER_PROFILE`, and
`CONTEXTBRIDGE_ADAPTER_ENDPOINT_ID` environment variables. Temporary state is
removed after the run and captured process output is bounded and excluded from
the report.

The v1 report checks that:

- an authentication failure is terminal and never causes a credential or
  protocol downgrade;
- an explicit HTTP failure is not automatically replayed;
- a rejected or expired lease is not progressed, completed, replayed, or
  replaced with newly polled work;
- profile discovery, heartbeat, endpoint-pinned polling, lease fencing,
  bounded progress and completion stay on the scoped v2 surface;
- a later heartbeat renews the original endpoint capability;
- the adapter claims the lease before completion; and
- the adapter rechecks lease authority, observes a cancellation, and stops
  without completion or polling new work;
- a restarted adapter follows a replacement lease's fresh generation and
  opaque capability instead of reusing stale fencing material; and
- a disconnect after completion bytes arrive is treated as ambiguous and is
  neither replayed nor followed by polling for new work.

Use `--profile-file` and `--job-file` for a real adapter's bounded driver
configuration and job shape. The job **must be side-effect-free and safely
repeatable**: the current suite can execute it four times across successful,
cancellation, replacement-lease, and ambiguous-completion scenarios. The
harness does not sandbox the operator-selected executable or block its
provider/network access. Run it in the isolation the adapter normally requires,
and never point it at a destructive profile.

The machine-readable report schema is
[`adapter-conformance-v1.schema.json`](schemas/adapter-conformance-v1.schema.json).
A passing result is free, self-run, point-in-time evidence—not certification,
a security audit, provider-quality validation, or project endorsement.
