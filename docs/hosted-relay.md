# Hosted Relay direction

ContextBridge remains local-first. A future Hosted Relay would operate the
coordination layer; it would not move a customer's models, API credentials, or
worker compute into a mandatory vendor cloud.

    producer or application
              |
              v
    optional managed relay
              |
              +-- home worker
              +-- office GPU worker
              +-- private VPS
              +-- customer cluster
              +-- local or API-backed engines

The intended product promise is:

> Your compute stays yours. We can operate the coordination layer for you.

Self-hosting must remain fully functional. A hosted offering would sell
operational convenience: TLS, updates, availability, monitoring, backups,
restore, and support. It must not depend on intentionally weakening the
self-hosted core.

## What exists today

- Outbound worker connections without an inbound worker port.
- Scoped relay and worker credentials.
- Producer and tenant ownership checks.
- Bounded queue, request, artifact, and cost controls.
- Optional E2EE for prompt and result payloads between a producer and a
  reserved worker.
- Durable leases, receipts, cancellation, and fail-closed ambiguous execution.

E2EE does not hide the coordination metadata needed to authenticate, schedule,
meter, and complete work. The project does not claim zero knowledge.

## Production multi-tenant gates

A paid shared service must not be described as production-ready until it has
evidence for all of the following:

- account identity, roles, organization membership, and recovery;
- tenant isolation across every API, queue, receipt, artifact, and log path;
- per-tenant encryption and key separation, rotation, revocation, and recovery;
- hard quotas, billing evidence, reservations, and unknown-cost handling;
- rate limiting, abuse detection, an acceptable-use process, and emergency
  suspension that cannot cross tenant boundaries;
- explicit retention, deletion, export, and legal-hold behavior;
- encrypted backups plus regularly exercised restore and migration drills;
- regional hosting and documented data-flow boundaries;
- privacy terms, a data-processing agreement, and subprocessors;
- monitoring, SLOs, incident response, customer notification, and audit export;
- zero-downtime upgrade and rollback evidence; and
- long-duration soak, chaos, capacity, and disaster-recovery results.

This is a readiness checklist, not a current compliance, availability, or SLA
claim. Commercial terms and pricing are intentionally separate from the open
protocol and self-hosted core.
