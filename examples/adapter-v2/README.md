<!-- SPDX-License-Identifier: Apache-2.0 -->

# Minimal adapter v2 reference

This dependency-free Node.js adapter demonstrates the public
`contextbridge.adapter.v2` contract without importing ContextBridge internals.
It is intentionally provider-neutral: for one leased job it returns the fixed
text `REFERENCE-ADAPTER-OK` instead of calling a model.

It demonstrates:

- a dedicated scoped adapter token, never the operator token;
- profile discovery and endpoint-capability registration;
- endpoint-pinned polling;
- lease generation plus opaque lease capability on every action;
- lease-authority checks before claim and before completion;
- claim-before-side-effect semantics;
- bounded progress and completion; and
- no automatic retry after an HTTP response or unknown transport outcome.

Configure one profile and principal:

```yaml
routes:
  reference:
    provider: adapter
    adapter_profile: reference
    task: generation

providers:
  adapter:
    auth_mode: scoped
    lease_seconds: 90
    principals:
      reference:
        token: ${CONTEXTBRIDGE_ADAPTER_TOKEN}
        allowed_profiles: [reference]

adapter_profiles:
  reference:
    label: Reference adapter
    driver: reference
```

Start ContextBridge, then run the adapter in a separate terminal:

Set `CONTEXTBRIDGE_ADAPTER_TOKEN` through the same private environment or
secret manager used by the config's `${CONTEXTBRIDGE_ADAPTER_TOKEN}` reference.
Then run:

```sh
CONTEXTBRIDGE_URL=http://127.0.0.1:32145 \
CONTEXTBRIDGE_ADAPTER_PROFILE=reference \
node examples/adapter-v2/reference-adapter.mjs
```

Submit one job using route `reference`. The adapter exits after completing that
job, which keeps the example deterministic. It checks authority before crossing
the claim boundary and again before completion, so a cancelled job is not
reported as successful. A production adapter would additionally maintain
heartbeats, renew and recheck authority during long-running provider work, bound
provider I/O, and stop automatic execution after an ambiguous post-claim
disconnect.

Run the independent client tests with:

```sh
node --test examples/adapter-v2/reference-adapter_test.mjs
sh scripts/test-reference-adapter-e2e.sh
```

The second command builds the real public ContextBridge binary, starts an
isolated loopback service with scoped credentials, runs this out-of-tree Node
adapter, runs Adapter Conformance v1, submits one bounded job, and verifies the
terminal result. It needs no model, provider account, Internet connection, or
privileged API.

You can run only the black-box suite with:

```sh
contextbridge adapter conformance \
  --adapter node \
  --arg ./examples/adapter-v2/reference-adapter.mjs \
  --profile reference \
  --working-directory .
```

Passing this example test is not adapter certification. The suite is bounded,
self-run evidence for the tested executable and moment; the actual core also
has adversarial tests for principal isolation, lease fencing, malformed
progress and bounded artifacts.
