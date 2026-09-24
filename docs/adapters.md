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
identifier. Private and third-party adapters map their own mechanics onto this
small lifecycle without adding implementation-specific code to the core.
