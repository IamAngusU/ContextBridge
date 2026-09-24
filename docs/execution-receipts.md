# Execution receipts

ContextBridge keeps three receipt claims separate:

1. a SHA-256 checksum detects accidental changes to the receipt file;
2. an optional Ed25519 signature authenticates the exact schema and evidence
   to an explicitly selected operator key; and
3. an optional online comparison proves that the evidence still matches the
   current authenticated relay record.

A checksum is not a signature. A valid operator signature is not a
`ContextBridge Verified` badge or project endorsement. A live comparison is
not required for offline authenticity, but it is the stronger freshness check
while the retained relay record still exists.

## Create an operator signing identity

Create one private signing key and its distributable public trust key:

```sh
contextbridge cluster receipt keygen \
  --private-out ./relay-receipt-private.json \
  --public-out ./relay-receipt-public.json \
  --key-id relay-2026-01 \
  --issuer "Example Operator"
```

Both output paths must be new and different. Keep the private file secret and
outside a web root, repository, artifact directory, and ordinary backups that
are shared with users. Distribute the public file through an authenticated
channel. ContextBridge never trusts an embedded or newly discovered signer
automatically.

## Export and verify

Export a signed v2 receipt while the terminal job is retained by the relay:

```sh
contextbridge cluster receipt export \
  --config ./config.yml \
  --signing-key ./relay-receipt-private.json \
  --out ./job-receipt.json \
  JOB_ID
```

Verify it later without relay access:

```sh
contextbridge cluster receipt verify \
  --file ./job-receipt.json \
  --trust-key ./relay-receipt-public.json \
  --offline
```

Omit `--offline` and supply the relay configuration/token to require both the
signature and equality with the current authenticated record:

```sh
contextbridge cluster receipt verify \
  --config ./config.yml \
  --file ./job-receipt.json \
  --trust-key ./relay-receipt-public.json
```

Unsigned v1 receipts remain supported. They can pass checksum integrity and a
live relay comparison, but the CLI refuses to describe them as independently
authentic offline evidence.

## What is signed

The signature uses a domain-separated canonical JSON message containing the
exact receipt schema and evidence object. The evidence binds the job ID,
contract, requested and observed selection, requirements and policy digests,
payload/result envelope digests, routing evidence, attempt counts, usage,
verified artifact-byte digests, stable failure state, node, and timestamps.
A signature copied to another job ID or modified evidence does not verify.

For an E2EE job, the receipt contains only coordination metadata and digests
already visible at the relay boundary. Signing does not decrypt or add prompt,
result, artifact-name, session, owner, or tenant content.

Key rotation and revocation remain operator responsibilities. Verifiers must
select the intended public key explicitly and retain their own record of which
key was trusted at verification time.
