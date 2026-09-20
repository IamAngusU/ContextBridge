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

Do not expose the local loopback service publicly. Put a relay behind HTTPS,
use separate scoped tokens, and keep configuration files readable only by the
service account.
