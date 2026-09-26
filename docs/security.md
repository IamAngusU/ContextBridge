# Security model

ContextBridge assumes prompts, model output, adapter telemetry, downloaded
metadata, job files, and remote API responses are untrusted.

Core controls include scoped bearer tokens, constant-time token comparison,
bounded request bodies, strict identifiers, idempotency, durable leases,
producer/tenant ownership checks, optional E2EE, path confinement, regular-file
checks, non-overwriting artifact saves, digest verification, cost reservation,
and execution-bound agent approval.

The system does not execute commands returned by a model. Agent plans are data,
not shell scripts. Named automatic authorities are operator-owned allow-lists;
a planner cannot widen them.

Do not expose the local loopback service publicly. A worker's `local_url` is
enforced as an actual loopback HTTP(S) URL before its local bearer token can be
used. Put a relay behind HTTPS, use separate scoped tokens, and keep
configuration files readable only by the service account.

E2EE is a payload boundary, not an anonymity claim. The relay still processes
the coordination metadata required to authenticate, schedule, meter, and
complete a job. Marketing and operator documentation must name the protected
payloads rather than describe the entire service as zero knowledge.

E2EE can be selected per native cluster job. An administrator can additionally
issue a producer credential with `require_e2ee`; cleartext job and pipeline
admission under that credential then fails closed with
`privacy.e2ee_required`. The credential rule prevents accidental downgrade,
but it does not hide coordination metadata, the ciphertext length, or content
from the selected worker and any downstream provider the worker invokes.
See [Privacy boundaries](privacy.md).

The authenticated producer subject owns jobs. `tenant_id` is otherwise a
caller-selected logical namespace and execution-policy selector, not proof of
customer identity. Administrators can issue a producer credential with
`allowed_tenants`; the relay then binds a single omitted tenant automatically
or rejects values outside the credential scope with
`scope.tenant_forbidden`, before policy selection. Applications still
authenticate and authorize their own users. See
[Producer identity and tenant labels](tenancy.md).

For an E2EE failure, the relay receives only a bounded stable failure code and
a generic failure label. Provider and local-runtime diagnostic text stays at
the worker/operator boundary because it can reflect decrypted payload content.
Plaintext jobs retain their existing relay-visible diagnostic text.
