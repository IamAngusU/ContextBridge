# Customer-controlled protected pools

ContextBridge can keep a hosted relay outside the trust boundary for sensitive
direct jobs. The optional pool-authority contract combines two checks:

1. the producer verifies a customer-signed certificate for the exact worker
   X25519 encryption key before encrypting; and
2. the worker verifies a customer signature over the exact E2EE ciphertext and
   execution context before doing any local work.

The relay carries public certificates and signatures. It never needs the pool
authority file or its Ed25519 private key.

This is an opt-in boundary. An unconfigured installation behaves exactly as it
did before and pays no signature cost.

## Fast setup

Create the authority on a customer-controlled system:

```text
contextbridge cluster pool init --id acme-private --out ./data/acme-pool-authority.json
```

Set the generated path in the configuration used for worker pairing and by
customer-controlled producers:

```yaml
cluster:
  pool_authority_file: ./data/acme-pool-authority.json
```

Pair or re-pair each protected worker normally. Pairing signs the newly created
worker encryption public key and stores only the public certificate in the
worker identity and relay database. On a separate worker machine, the authority
file may be removed after pairing; the worker process does not need it to run.

Keep the authority file on the producer backend that submits protected work.
`cluster submit` and `cluster chat` automatically enable E2EE when this setting
is present. Issue the producer credential with `require_e2ee` as an additional
relay-side fail-closed rule.

Never copy the authority file to the hosted relay. Back it up like an account
root key. Worker certificates deliberately do not expire, so decommissioning a
worker requires rotating the authority and re-pairing the remaining protected
workers. Replacing or losing the authority also requires re-pairing them.

## What a custom UI or backend does

The authenticated OpenAPI document exposes `pool_certificate` on the assignment
and `pool_authorization` on job submission. A backend implements the same
bounded flow as the native client:

1. `POST /v1/cluster/assign` with tenant, requirements, `pool_id`, and the
   pinned `pool_authority_public_key`. The latter two are public routing hints;
   local producer verification remains authoritative.
2. Verify `assignment.pool_certificate` with the customer's pinned authority
   public key, including its exact `worker_public_key` binding.
3. Build the normal E2EE encryption context and seal the payload to
   `assignment.public_key`.
4. Sign `contextbridge.pool-job.v1`, which binds the pool ID, expiry, complete
   encryption context and SHA-256 of the serialized sealed envelope.
5. `POST /v1/cluster/jobs` with the one-time assignment values, sealed payload
   and `pool_authorization`.

Use a backend-for-frontend for browsers. Do not place the authority private key
or relay bearer credential in downloadable frontend JavaScript.

## Security result

Assuming the customer controls the producer key, protected worker and local
ContextBridge process, a hosted relay operator cannot:

- substitute an operator-owned encryption key without producer verification
  failing;
- decrypt the protected prompt or result;
- change the authenticated tenant, requirements, policy decision, node, job ID
  or ciphertext without invalidating the job authorization; or
- create a new valid job for the protected worker without the customer key.

The checks are local Ed25519 operations. They add no network round-trip and no
per-token/model-loop work. The normal model runtime remains the dominant cost.

The relay can still deny service, delay or drop work, choose among workers that
the same customer authority certified, observe coordination metadata, and
replay the exact authorized envelope during its bounded validity. Existing
assignment fencing, duplicate suppression and the deterministic local job ID
limit accidental re-execution, but this is not a claim that a hostile relay is
an availability or traffic-analysis boundary.

## Deliberate boundary for pipelines

Relay pipelines are currently a cleartext orchestration feature: the relay
renders each later step from earlier output. A relay that is not trusted with
content cannot perform that function, and a relay-created child cannot carry a
new customer signature.

Therefore a certified protected worker fails closed on plaintext, pipeline,
agent, console or other unsigned work. Those product features remain available
on ordinary workers and pools. The relay's normal scheduler excludes certified
workers from ordinary work, while a protected assignment explicitly selects
the customer pool. Route sensitive direct jobs to a protected group and
orchestration jobs to a separate ordinary group. Claiming both opaque
relay-side pipelines and zero relay access would require an online customer
signer for every generated step, adding latency and availability coupling that
this design intentionally avoids.

## Trust statement

The accurate sales claim is:

> In customer-controlled protected-pool mode, the hosted relay has no key that
> can decrypt protected direct-job content or authorize new protected work.

Do not shorten this to “the operator can never see anything.” Coordination
metadata remains visible, cleartext features have their documented trust model,
and compromise of the customer's producer or worker remains outside this
boundary.
