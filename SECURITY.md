# Security Policy

## Supported Versions

Security fixes target the latest released minor version. Upgrade to the newest release before reporting a behavior that may already be fixed.

## Reporting A Vulnerability

Send a private report to `hello@angusu.de` with:

- affected ContextBridge version and operating system
- provider and browser involved
- minimal reproduction steps
- expected and observed security boundary
- whether tokens, local files, or user data may be exposed

Do not include live credentials or private submitted content. You will receive an acknowledgement as soon as the report is reviewed.

## Scope

Reports are especially useful for:

- token or origin authorization bypasses
- access to tabs that were not explicitly selected
- arbitrary command execution
- path traversal or unsafe folder handling
- remote code in extension packages
- model output bypassing decision normalization

Provider account rules, upstream browser behavior, and unsupported public exposure of the localhost service are outside the direct project boundary, but actionable hardening reports are still welcome.
