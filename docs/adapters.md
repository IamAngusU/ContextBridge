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

## Local protocol

Adapters connect to the authenticated local service, which binds to loopback by
default. Every request uses the local service bearer token. The protocol is
pull-based, so the core does not need to launch or supervise an adapter.

| Method | Endpoint | Purpose |
| --- | --- | --- |
| `GET` | `/v1/adapter/profiles` | Read operator-configured profile labels, drivers, and options. |
| `POST` | `/v1/adapter/heartbeat` | Publish bounded process and endpoint capability state. |
| `GET` | `/v1/adapter/jobs/next?profile=ID&endpoint_id=N` | Long-poll for one compatible lease; `204` means no work. |
| `GET` | `/v1/adapter/jobs/JOB/lease` | Check whether the supplied lease generation still owns the job. |
| `POST` | `/v1/adapter/jobs/JOB/lease` | Renew an unexpired lease. |
| `POST` | `/v1/adapter/jobs/JOB/progress` | Publish monotonic, bounded progress. |
| `POST` | `/v1/adapter/jobs/JOB/claim` | Cross the no-automatic-retry boundary. |
| `POST` | `/v1/adapter/jobs/JOB/release` | Return an untouched lease to the queue. |
| `POST` | `/v1/adapter/jobs/JOB/complete` | Submit the normalized result and verified artifact bytes. |

Lease-scoped calls include
`X-ContextBridge-Lease-Generation: <unsigned-integer>`. Before its first
external side effect, an adapter claims the lease with one generic lifecycle
stage: `prepare`, `mutate`, or `commit`. Once claimed, an ambiguous disconnect
becomes observation-only; ContextBridge will not silently repeat work that may
already have happened.

Heartbeat input is bounded to 128 KiB, 16 endpoints, 50 model choices per
endpoint, and 20 reasoning choices per endpoint. Profile IDs are safe opaque
identifiers. A heartbeat can report only scheduling evidence; it cannot grant
itself policy, credentials, budget, or queue ownership.

The contract deliberately contains no driver implementation or vendor
identifier. Private and third-party adapters map their own mechanics onto this
small lifecycle without adding implementation-specific code to the core.
