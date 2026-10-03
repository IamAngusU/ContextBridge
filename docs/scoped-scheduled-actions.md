<!-- SPDX-License-Identifier: Apache-2.0 -->

# Scoped scheduled adapter actions

Scoped scheduled actions let an out-of-tree channel request a future external
side effect without receiving the relay administrator token and without
keeping a hidden timer of its own. This is a separate contract from local
`contextbridge schedule` jobs.

The relay stores only normalized timing and opaque references. It never stores
the destination address, message body, generated tool call, or provider
credential in the scheduled-action record. At the due time it creates one
ordinary `provider: adapter` job. The existing v2 adapter lease and claim
protocol remains the side-effect boundary.

## Authority model

Scheduled actions are disabled unless an administrator issues a producer
credential with a versioned policy. Each policy target binds all of these
values together:

- the stable `adp_...` presence UID;
- one local v2 execution profile;
- one local v2 adapter principal;
- the allowed action kinds; and
- opaque `dst_...` destination references.

The producer selects the target UID, action kind and destination reference. It
cannot mix a UID with another target's profile or principal. Presence proves
fresh liveness only; it never grants execution authority.

Do not place the policy-bearing producer credential in the executor process.
Issue a second producer credential with the same subject and no
`scheduled_actions` policy for presence heartbeats. The subject keeps the
stable UID aligned, while only the channel credential can preview and confirm
actions.

Example `scheduled-action-policy.json`:

```json
{
  "schema": "contextbridge.scheduled-action-policy.v1",
  "targets": [
    {
      "adapter_uid": "adp_0123456789abcdef0123456789abcdef",
      "adapter_profile": "message-delivery",
      "adapter_principal": "message-adapter",
      "action_kinds": ["message.text"],
      "destination_refs": ["dst_0123456789abcdef0123456789abcdef"]
    }
  ],
  "max_active": 8,
  "max_horizon_seconds": 604800,
  "min_interval_seconds": 900,
  "max_occurrences": 32,
  "max_delivery_window_seconds": 3600
}
```

Create the producer credential on the relay:

```sh
contextbridge cluster token create \
  --role producer \
  --subject channel-primary \
  --providers adapter \
  --max-priority 20 \
  --scheduled-actions-policy ./scheduled-action-policy.json
```

New configurations include the neutral `scheduled_action` task in the relay
policy allowlist. Existing configurations with an explicit
`cluster.policies.allowed_tasks` list must add that exact task themselves;
ContextBridge never broadens an operator-owned allowlist during an upgrade.
Configure the bound adapter route for that same exact task (for example with
`contextbridge adapter setup PROFILE --task scheduled_action ...`). Ordinary
job submission, encrypted assignment reservation, and pipeline steps cannot
use this relay-reserved task: only a confirmed due action receives the
non-serializable internal admission capability. The side-effect adapter must
also reject every payload that is not the exact
`contextbridge.scheduled-adapter-action.v1` reference envelope it expects.

The same option is available to `contextbridge integrate relay`. The policy is
strict JSON: duplicate, misspelled, case-folded or unknown properties are
rejected. The reusable schema is
[`scheduled-action-policy-v1.schema.json`](schemas/scheduled-action-policy-v1.schema.json).

Do not combine this credential with `require_e2ee`. E2EE hides job payload
bytes from the relay, while this contract requires the relay to construct a
small cleartext reference envelope at the due time. The envelope contains no
raw destination or message content, but its scheduling metadata remains
relay-visible.

## Preview and explicit confirmation

The channel resolves natural language outside this security boundary. It
stages the actual content in its own bounded store, receives a high-entropy
`ref_...` payload reference, and submits a normalized request. Example
`action.json`:

```json
{
  "schema": "contextbridge.scheduled-action-request.v1",
  "adapter_uid": "adp_0123456789abcdef0123456789abcdef",
  "action_kind": "message.text",
  "destination_ref": "dst_0123456789abcdef0123456789abcdef",
  "payload_ref": "ref_fedcba9876543210fedcba9876543210",
  "start_at": "2026-10-03T13:00:00+02:00",
  "timezone": "Europe/Berlin",
  "delivery_window_seconds": 300,
  "priority": 10
}
```

Preview, inspect the normalized local time, and confirm with the exact same
producer credential:

```sh
contextbridge cluster scheduled-action preview \
  --file ./action.json \
  --token-file ./producer-token.json

contextbridge cluster scheduled-action confirm sact_ID \
  --token-file ./producer-token.json
```

A preview expires after ten minutes and never dispatches by itself. Another
credential with the same subject cannot view or confirm it. The `start_at`
offset must match the explicit IANA timezone at that instant; `Local` and
implicit machine timezones are rejected.

Opaque references are unguessable selectors, not bearer credentials. The
adapter must bind every staged payload to the authenticated owner/tenant and
destination scope, apply a bounded expiry, and consume or otherwise fence the
exact occurrence before its side effect. It must never resolve a `ref_...`
globally merely because another producer supplied the same value.

Because `max_active` is enforced per authenticated owner and tenant, a
scheduled-action credential without `allowed_tenants` may use only the empty
owner-level tenant scope. To schedule per tenant, bind the permitted tenant
IDs on the producer credential; caller-invented tenant labels cannot be used
to split the capacity limit. Durable history is additionally capped at 1,024
records per owner across credentials and tenants (and 10,000 records per
relay), so terminal preview/cancel churn from one producer cannot consume the
entire scheduler store.

Manage only the records owned by that exact credential:

```sh
contextbridge cluster scheduled-action list --status active --token-file ./producer-token.json
contextbridge cluster scheduled-action show sact_ID --token-file ./producer-token.json
contextbridge cluster scheduled-action cancel sact_ID --token-file ./producer-token.json
```

The request and response schemas are
[`scheduled-action-request-v1.schema.json`](schemas/scheduled-action-request-v1.schema.json)
and [`scheduled-action-v1.schema.json`](schemas/scheduled-action-v1.schema.json).
The authenticated relay OpenAPI document exposes the same routes and bounds.
The protected Prometheus endpoint exports only fixed-cardinality
`contextbridge_scheduled_actions{state="..."}` gauges; it never labels a
schedule, owner, tenant, adapter, destination, or payload reference.

## Dispatch and failure semantics

At each due time the relay rechecks:

1. the exact original credential still exists and is not expired or revoked;
2. its current durable policy still authorizes the complete target binding;
3. the target adapter is not administratively disabled;
4. a fresh ready presence lease explicitly advertises the descriptive
   `scheduled-action` capability; and
5. normal tenant, provider, priority, queue, rate and execution policy still
   permit the job.

Only then does the relay atomically create a one-attempt adapter job. The job
is pinned to the policy's v2 principal and profile. Its metadata contains a
`contextbridge.scheduled-adapter-action.v1` envelope with schedule ID,
target adapter UID, occurrence, opaque references, due time and expiry. The
adapter must reject an envelope for any other UID, recheck its lease, and call
the v2 `claim` endpoint immediately before the external side effect.
The worker supplies the authenticated `contextbridge_owner_subject` and
`contextbridge_tenant_id` beside the envelope on the local leased job. The
adapter must require those execution-only values to match the owner and tenant
recorded when the opaque destination and payload references were staged.
The inner envelope has a reusable strict schema:
[`scheduled-adapter-action-v1.schema.json`](schemas/scheduled-adapter-action-v1.schema.json).

The action points to the durable job, whose bounded result is the delivery
receipt. A definitive job failure stops the action. If a worker disconnects,
times out, or is cancelled after the external side effect may have begun, the
action becomes `unknown` and is not replayed. ContextBridge does not claim
exactly-once delivery across an external provider's ambiguous boundary.

Intervals are elapsed-time schedules with a fixed number of occurrences, not
calendar recurrence rules. Missed occurrences outside their delivery windows
are skipped without an unbounded catch-up burst. Only one occurrence is in
flight at a time.

Schedule-layer terminal failure codes are stable machine-readable values (an
adapter job failure may instead propagate one of the manifest's ordinary
runtime failure codes):

| Code | Meaning |
| --- | --- |
| `scheduled_action.credential_inactive` | The exact producer authority expired, was revoked, or no longer matched. |
| `scheduled_action.policy_denied` | Current relay execution policy denied the generated adapter job. |
| `scheduled_action.delivery_window_expired` | No side effect began before the bounded delivery deadline. |
| `scheduled_action.cancel_ambiguous` | Cancellation raced with a possibly started side effect. |
| `scheduled_action.delivery_timeout_ambiguous` | An assigned or running delivery crossed its deadline. |
| `scheduled_action.job_missing` | Durable action state referenced a missing job and failed closed. |
| `scheduled_action.job_cancelled` | The dispatched adapter job was cancelled before reconciliation. |
| `scheduled_action.envelope_failed` | The relay could not construct its bounded reference envelope. |
| `scheduled_action.dispatch_failed` | An internal dispatch failure prevented a safe attempt. |

For a completed or adapter-reported failed attempt, `last_job_id` points to the
ordinary durable job and its bounded result/failure evidence. A transient
queue, rate, disabled-adapter, or presence condition remains `active` only
inside the delivery window and exposes `waiting_reason` rather than claiming a
delivery occurred.

## Deliberate non-goals

- No raw telephone number, address or message text is accepted as a scheduled
  destination or payload.
- No discovered adapter becomes trusted automatically.
- No schedule grants generic shell, arbitrary tool or operator authority.
- No outbound call or other provider action exists unless an installed
  adapter, its own provider configuration and the credential policy all allow
  the explicit action kind.
- The relay does not parse free-form language into authorization decisions.

An absent, expired or broken optional adapter affects only its own action; it
does not prevent ordinary local or pool jobs from running.
