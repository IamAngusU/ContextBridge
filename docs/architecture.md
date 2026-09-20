# Architecture

ContextBridge separates four roles:

1. **Producer** submits a bounded job and reads its result.
2. **Relay** authenticates, queues, schedules, and records authoritative state.
3. **Worker** advertises bounded capabilities and executes assigned work.
4. **Engine or adapter** performs one selected operation on the worker.

Workers initiate outbound connections. A producer never receives a worker's
local credential or direct filesystem access. The relay sees scheduling
metadata. For an E2EE job, prompt and result payloads can remain opaque to the
relay, but job identifiers, timing, routing requirements, worker assignment,
cost evidence, sizes, and other coordination metadata are not described as
secret. ContextBridge does not claim that the relay is zero knowledge.

Scheduling follows this order:

```text
validate request
  -> enforce tenant and execution policy
  -> filter by hard capabilities
  -> reserve cost and one worker lease
  -> dispatch once
  -> verify output and artifacts
  -> persist receipt and terminal state
```

An ambiguous post-dispatch failure is not retried automatically because the
engine may already have performed the work.

## State authority

- The relay owns queue, assignment, cancellation, and terminal job state.
- The worker owns local runtime and hardware observations.
- Engines own model execution but do not define job success.
- ContextBridge verifies output contracts, artifact bytes, and digests.
- Adapters provide bounded endpoint capability and completion evidence; the
  core does not embed adapter-specific logic.
