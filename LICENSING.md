# Licensing

## Version boundary

The published v0.6.0 through v0.6.3 releases remain available under the MIT
License that accompanied them. Those permissions are not withdrawn. The
current AGPL release builder rejects any new version below v0.7.0.

The ContextBridge v0.7.0 core and current main branch are licensed under
AGPL-3.0-only, copyright 2026 Angus Uelsmann. The full terms are in
[LICENSE](LICENSE), and the project copyright notice is in [NOTICE](NOTICE).

The AGPL does not prohibit commercial use. In particular, an operator that
modifies the AGPL-covered program and lets users interact with that modified
version over a network must offer those users access to the Corresponding
Source of that version under the AGPL. This summary is not a substitute for the
license text or legal advice.

## Permissive integration surfaces

The following files are independently licensed under Apache-2.0 so other
software can implement the documented boundary without adopting the core's
license merely by copying these materials:

- docs/schemas/**
- docs/adapters.md
- docs/compatibility.md
- docs/verification.md
- examples/**

The Apache-2.0 text is in
[LICENSES/Apache-2.0.txt](LICENSES/Apache-2.0.txt). Directory notices and SPDX
identifiers make these exceptions explicit. The Go relay, worker, scheduler,
CLI, engines, and other implementation code remain part of the AGPL-covered
core. No separately versioned SDK is claimed until one is actually published
as a distinct package or repository.

External adapters communicate through a documented process or network
boundary and may choose their own license. Whether a particular combined work
is derivative is a fact-specific legal question; this document does not grant
permissions beyond the stated licenses.

## Commercial licensing

A commercial alternative is planned for organizations that require negotiated
proprietary modification, embedding, redistribution, warranty, support, or
other contractual terms. No commercial license is granted by this statement;
one exists only when signed by the copyright holder and the customer.

A negotiated commercial grant may be time-limited, renewed, or scoped to a
specific product or deployment. That contractual validity is separate from
the permanent license carried by each published source release. The public
core is not being moved to a delayed-open-source license: a calendar-only
boundary is ambiguous about which immutable bytes received which rights and
would weaken the project's open interoperability position.

Before any external core code contribution is accepted, the project will use a
professionally reviewed contributor agreement that expressly supports this
dual-licensing model. Issues, security reports, design discussion, and
documentation feedback remain welcome. See [CONTRIBUTING.md](CONTRIBUTING.md).

## Product identity

Copyright and trademark are separate. Honest compatibility and origin
statements remain welcome, while modified products must not imply that they are
official ContextBridge releases. See [TRADEMARKS.md](TRADEMARKS.md).

Free self-run conformance and optional issuer-backed verification are also
separate from code licensing. A signed verification statement may expire and
is bound to exact product bytes and scope; it grants no software-license or
trademark rights. See [docs/verification.md](docs/verification.md).
