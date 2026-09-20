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

ContextBridge is MIT licensed. It does not claim certification, legal
compliance, or suitability for a regulated deployment without the operator's
own review and controls.
