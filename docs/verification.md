<!-- SPDX-License-Identifier: Apache-2.0 -->

# Verification and conformance

ContextBridge keeps two different kinds of evidence separate:

1. **Conformance is free and self-run.** Anyone can run the published relay and
   worker checks, keep the JSON reports, and accurately state which versioned
   contract passed.
2. **ContextBridge Verified is issuer-backed, signed, and time-bounded.** It is
   a foundation for a future reviewed verification service. No paid program,
   price, SLA, or general availability is claimed until the project publishes
   those terms.

The second layer does not make the first one less useful. A signed statement
binds a reviewer identity to exact evidence; it does not turn an open contract
test into a paywall.

## What a statement proves

A v1 statement binds all of the following under an Ed25519 signature:

- the issuer and verification ID;
- the tested vendor, product, version, and artifact SHA-256;
- the exact ContextBridge version and source commit used for testing;
- exact contract and conformance identifiers;
- the names and SHA-256 digests of the reviewed evidence files; and
- an issuance and expiry timestamp.

The validity window cannot exceed 366 days. A material change to the tested
artifact, product version, scope, or evidence requires a new statement even if
the old calendar window has not ended. This deliberately uses **both** an exact
technical boundary and a time boundary: time alone cannot identify the bytes
that were tested, while a version alone should not create a permanent badge.

The published JSON shape is
[`verification-statement-v1.schema.json`](schemas/verification-statement-v1.schema.json).
The schema is an interchange description; the CLI additionally enforces
closed objects, bounded sizes, sorted unique identifiers, a maximum validity
window, exact key identity, signature validity, and current time validity.

## Verify a signed statement

The issuer's public key is a small JSON document:

```json
{
  "schema": "contextbridge.verification-trust-key.v1",
  "key_id": "contextbridge-example-2026",
  "issuer": "ContextBridge Project",
  "algorithm": "ed25519",
  "public_key": "BASE64_ED25519_PUBLIC_KEY"
}
```

Verify the signature and validity window:

```sh
contextbridge verification verify \
  --file ./verification-statement.json \
  --trust-key ./contextbridge-verification-key.json
```

Re-hash every evidence file as well:

```sh
contextbridge verification verify \
  --file ./verification-statement.json \
  --trust-key ./contextbridge-verification-key.json \
  --artifact ./tested-product.tar.gz \
  --require-artifact \
  --evidence-dir ./verification-evidence \
  --require-evidence \
  --json
```

Evidence names are single portable file names. The verifier opens them beneath
the selected directory, rejects traversal and non-regular files, caps each
file at 64 MiB, and compares the bytes with the signed SHA-256. A selected
subject artifact is streamed and bounded at 8 GiB before its signed digest is
accepted.

The public verifier contains no issuer private key. A future verification
service keeps issuance and review authority outside the distributable binary;
publishing the verifier lets customers, CI, and auditors check a statement
without trusting a hosted status page.

## Accurate claims

Use `passes ContextBridge Relay Conformance v1` for a current self-run report.
Use `ContextBridge Verified` only when an announced project program has issued
an unexpired statement for that exact product, artifact, and scope. Supplying
an arbitrary public key to the generic verifier proves only that the matching
key holder signed the statement; it does not make that issuer official.

Verification is not:

- a security certification or penetration test;
- a legal, privacy, GDPR, SOC 2, or regulatory attestation;
- a guarantee of model quality, provider availability, or future
  compatibility;
- an SLA; or
- permission to use project marks outside the project identity policy.

The initial offline format also has no revocation feed. Before a commercial
program launches, the issuer must publish key-rotation, early-revocation,
review, dispute, and renewal terms rather than implying that cryptographic
validity alone is a complete service.

See [Compatibility boundaries](compatibility.md) for the free conformance
commands and [the project identity policy](../TRADEMARKS.md) for badge and name
use.
