# Security policy

Please report suspected vulnerabilities privately through GitHub Security
Advisories for this repository. Do not include live tokens, private prompts,
model output, credentials, or personal data in a public issue.

Include:

- affected version and operating system;
- the smallest reproducible configuration;
- expected and observed behavior;
- whether the issue crosses a tenant, producer, worker, filesystem, cost, or
  execution-policy boundary;
- sanitized logs or a minimal proof of concept.

High-priority boundaries include authentication, E2EE context binding, lease
ownership, tenant isolation, artifact validation, path traversal, command
execution, update verification, cost enforcement, agent authority, and adapter
capability spoofing.

The current core is AGPL-3.0-only; published v0.6.0 through v0.6.3 releases
retain the MIT License that accompanied them. ContextBridge does not claim
certification, legal compliance, or suitability for a regulated deployment
without the operator's own review and controls.
