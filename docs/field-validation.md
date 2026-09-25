# Field validation and reproducible resilience evidence

ContextBridge separates three kinds of evidence:

1. deterministic unit/race/fuzz tests in the source tree;
2. a local, content-free durability proof anyone can reproduce;
3. topology-specific fault injection and long-running field evidence.

Do not promote an anecdote into a product claim. Record the exact version,
commit, topology, operating systems, action, expected result, observed result,
timestamps and redacted evidence for every public field result.

## Built-in local proof

Run this before a deployment or release candidate:

```bash
contextbridge cluster conformance resilience
```

It creates isolated temporary stores, sends no AI request and contacts no
network service. It currently proves these local durable-store invariants:

- relay authority retains one cluster identity and advances its epoch after a
  restart;
- a lost-response retry with the same producer-scoped idempotency key returns
  the original durable job after restart, while changed content is rejected;
- running work becomes terminally ambiguous after relay restart and is not
  silently requeued;
- an older relay epoch cannot advance the current assignment;
- a result has one terminal commit and survives a store restart byte-for-byte.

Save a machine-readable result next to the release evidence:

```bash
contextbridge cluster conformance resilience --json > resilience-proof.json
```

The report is `contextbridge.resilience-proof.v1` and explicitly states its
scope. Passing it does **not** prove network partitions, TLS, proxy behavior,
multi-relay consensus, provider behavior, worker process survival or a soak
test. Those require controlled field tests.

## Field evidence record

For every controlled fault, preserve:

- ContextBridge version and exact source commit;
- date, time, duration and time zone;
- relay, producer and worker topology;
- OS/runtime versions and worker IDs;
- redacted relevant configuration and policy;
- expected state before fault injection;
- exact injected fault;
- observed recovery time and terminal state;
- job IDs, event cursor, route evidence and receipt where applicable;
- PASS, FAIL or AMBIGUOUS;
- a follow-up issue for every unexpected result.

High-value scenarios include a 24-hour worker outage and automatic recovery,
lost submit response with exact idempotent replay, worker death during
execution with no duplicate dispatch, slot saturation with explainable
placement, mixed Windows/Linux workers, and 24/72-hour resource soaks.

Secure offline-LAN evidence additionally records:

- one-machine local inference with WAN physically absent;
- two-machine pinned TLS pairing and execution on an isolated switch;
- WAN removal during a local job without loss of local finality;
- simultaneous local and cloud routes where only the cloud route becomes
  unavailable;
- relay-address change without acceptance of a different TLS identity;
- forged discovery/advertisement and TLS-MITM rejection;
- worker sleep/resume and a 24-hour reconnect soak without hosted CB traffic.

The unit/integration suite exercises pinned TLS HTTP, pairing and WebSocket
transport entirely on loopback. It does not replace the isolated-switch and
WAN fault-injection evidence above.

Issue
[#12](https://github.com/IamAngusU/ContextBridge/issues/12) is the living,
fine-grained checklist. This document defines the evidence standard and the
reproducible local baseline; unchecked issue rows are not supported claims.
