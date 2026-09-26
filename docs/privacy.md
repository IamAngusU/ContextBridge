# Privacy boundaries

ContextBridge supports payload confidentiality. It does not currently claim
that a relay is zero knowledge.

## Modes

| Mode | How to select it | Relay can read prompt/result bytes | Downgrade protection |
| --- | --- | --- | --- |
| Cleartext | ordinary Job Contract v1 submission | yes | not applicable |
| Per-job E2EE | `cluster chat --e2ee` or `cluster submit --e2ee` | no | the job/result encryption context authenticates the assigned execution |
| Credential-required E2EE | issue the producer token with `--require-e2ee` and use an E2EE-capable client | no | relay rejects cleartext admission with `privacy.e2ee_required` |

Create a native-client credential whose jobs must be sealed:

```sh
contextbridge cluster token create \
  --role producer \
  --subject private-app \
  --require-e2ee
```

Or create a private server-side integration file without printing its token:

```sh
contextbridge integrate relay \
  --subject private-app \
  --require-e2ee \
  --write-env ./contextbridge-private.env
```

The second command only provisions the credential. The consuming application
must still implement ContextBridge's one-time assignment reservation and sealed
submission flow. A conventional cleartext HTTP example cannot use this token.

## What E2EE hides from the relay

- prompt and structured input payload bytes;
- attached input bytes carried inside the sealed payload;
- result text and result artifact bytes carried inside the sealed result;
- detailed worker/provider failure text that could reflect decrypted content.

The selected worker necessarily decrypts the payload to execute it. If that
worker calls a remote model API, that provider receives the content required by
the selected operation. E2EE is not an egress sandbox.

## What remains relay-visible

The relay needs bounded coordination data to authenticate, route, fence,
meter, retain, and complete work. This currently includes:

- producer subject and optional tenant label;
- job, assignment, worker, and relay authority identifiers;
- requested task, provider, model, group, hardware and capability constraints;
- session/routing requirements needed for placement;
- priority, attempts, status, timestamps, ciphertext size, and cancellation;
- policy decision, routing evidence, coarse usage/cost evidence, and failure
  category;
- selected worker and execution timing.

E2EE authenticates the outer fields that affect worker execution: job, node,
attempt, owner, tenant, requirements, and the policy decision. It does not
authenticate every scheduling or accounting field listed above. In particular,
priority, lifecycle timestamps, status projections, usage evidence, and other
relay-owned coordination state retain their normal relay trust boundary.
Authentication is not concealment: even the authenticated fields remain
visible. Applications should never place secrets in tenant, source, model,
group, session, or other routing metadata.

## Fail-closed credential rule

`require_e2ee` is stored with the producer credential. Enforcement happens in
both the relay contract path and durable store admission, so a caller cannot
weaken it by omitting a CLI flag or bypassing one HTTP handler. It is ignored
for no other role: assigning producer limits to an observer, node, or admin
credential is rejected.

Current pipeline execution uses cleartext step payloads and is therefore
rejected for an E2EE-required producer. Progress streaming is also unavailable
for encrypted jobs; clients receive the encrypted final result. These are
explicit compatibility limits, not silent downgrade paths.

## Combining privacy and egress policy

E2EE controls what the relay can read. `requirements.egress: local_only`
controls where a job may execute only when the relay execution policy is
enabled and the provider is operator-classified as local. They solve different
problems and can be combined:

```sh
contextbridge cluster chat \
  --provider ollama \
  --egress local_only \
  --e2ee \
  --artifacts off \
  --prompt "Summarize this private note."
```

For application-wide enforcement, combine a token issued with
`--require-e2ee`, `--providers ollama`, and `--egress local_only`. The E2EE
requirement remains effective even if execution policy is disabled; the egress
claim does not.

## Why this is not called zero knowledge

A literal zero-knowledge relay would require a materially different contract:
for example hidden or oblivious routing attributes, anonymous authorization,
traffic-size/timing protections, and a way to schedule and meter without the
current coordination evidence. Those changes conflict with several present
operator guarantees and are not implemented. ContextBridge therefore makes the
narrower, testable claim: **end-to-end encrypted job payloads with explicit,
relay-visible coordination metadata**.
