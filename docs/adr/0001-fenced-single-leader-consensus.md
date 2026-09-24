# ADR 0001: Fenced single-leader consensus

- Status: accepted for an opt-in prototype; not yet implemented or supported
- Date: 2026-09-24
- Scope: relay coordination state only

## Context

ContextBridge needs to survive loss of a relay without letting two relays
dispatch or commit the same durable job. Unconstrained active-active writes are
not acceptable for this state: conflicting masters could each assign one job
before they learn about the other write. That contradicts the existing rule
that uncertain execution is terminally ambiguous rather than silently replayed.

The public core must retain a zero-dependency standalone mode. HA is an opt-in
deployment mode for operators who can run an odd number of reliable, mutually
reachable coordination nodes.

Wire v3 already provides the prerequisite safety boundary: a stable durable
cluster identity, a monotonically increasing relay epoch, an exact assignment
generation, and worker/result fencing. It deliberately does not claim
replication, elections, or quorum.

## Decision

Implement the opt-in consensus plane on the deterministic
[`etcd-io/raft`](https://github.com/etcd-io/raft) core. Pinning begins with
`go.etcd.io/raft/v3` v3.7.0 (`b867cf13f6bc0dae21204302df97bc2355c3af55`),
but the dependency does not enter release builds until the storage/transport
implementation and the proof gates below are complete.

`etcd-io/raft` deliberately implements only the consensus state machine. CB
therefore owns a small, explicit integration around it:

- a separate durable Bolt WAL/hard-state/snapshot path, never the application
  store and never shared by two processes;
- one serialized `Ready` persistence/apply/send loop per relay voter;
- an authenticated mTLS peer transport with bounded frames, peer identities
  pinned to the configured membership, and no producer/worker bearer tokens;
- a versioned deterministic CB command FSM and checksummed snapshots;
- linearizable execution-affecting reads through `ReadIndex`; and
- explicit leadership transfer and one-at-a-time joint membership changes.

The larger integration burden is intentional. The Raft algorithm remains a
single-threaded deterministic state machine, while disk and network behavior
stay visible and testable in CB rather than being hidden behind a transport
fast path.

The coordination model is CP and single-writer:

```text
stable producer URL / reverse proxy
                 |
       +---------+---------+
       |                   |
   follower <---------> leader <---------> follower
       |                   |                   |
       +-------- replicated ordered log ------+
                           |
                 deterministic CB FSM
                           |
             worker pool (outbound connections)
```

Only a leader that has committed a fresh `AcquireLeaderEpoch` FSM command may
report `/leaderz` as `writable: true`. The resulting replicated epoch, rather
than a process-local belief or wall clock, becomes the `relay_epoch` in new
assignment fences. A follower and a deposed leader reject mutations.

## Replicated state

The ordered log contains only authoritative mutations:

- producer-scoped idempotent job admission;
- queue state, assignment generations, cancellation, terminal results, and
  execution receipts;
- worker pairing identity, token issue/revocation, and administrator drain;
- E2EE reservations and the locks/ownership records required to consume them;
- durable session placement and pipeline/schedule transitions whose replay
  changes execution decisions;
- membership and the committed ContextBridge leader epoch.

The log does not contain high-rate or reconstructable observations:

- metrics, logs, GPU samples, temperatures, and clocks;
- ephemeral connection objects and WebSocket buffers;
- transient UI presence;
- provider/model telemetry that is not itself an execution authorization.

Those remain local/eventually consistent. A newly elected relay must wait for
fresh worker connections and capability evidence; it must not dispatch from a
stale telemetry snapshot.

## Storage boundary

Raft log/stable data, snapshots, and ContextBridge FSM state use separate
paths. They are never separate writers to one Bolt file.

In HA mode, HTTP handlers, schedulers, and maintenance loops may not call
durable mutators directly. They submit a versioned command to the leader and
return success only after the command is committed and applied. The FSM is the
only writer to replicated application state. Standalone mode uses the same
command validation/apply layer locally so the two modes cannot drift into
different semantics.

Commands are bounded, schema-versioned, deterministic, and contain no wall
clock reads, random generation, network calls, or host-dependent behavior in
`Apply`. IDs, timestamps, policy decisions, and bounded evidence are fixed by
the leader before proposal and validated again by the FSM.

## Reads and proxying

- Mutations go only to the writable leader.
- A read that authorizes or influences execution uses the leader after a
  barrier/leadership verification.
- Explicit diagnostic reads may be served from followers and must identify
  themselves as potentially stale.
- The ordinary producer sees one operator-owned URL. A reverse proxy routes
  mutations only to a node whose `/leaderz` is HTTP 200 with
  `writable: true`; `/livez` is never sufficient.
- CB will not require every PHP/shared-hosting client to implement Raft
  discovery or redirect logic.

## Membership and transport

- Supported production shape starts at three voters; five is optional. Two
  voters are not presented as HA because loss of either removes quorum.
- Membership changes are explicit administrator operations and use the
  library's configuration-change APIs with the expected previous index.
- New nodes join as non-voters/staging members, catch up, and are deliberately
  promoted. No mDNS or ambient LAN discovery may create a voter.
- Consensus transport uses a distinct authenticated TLS/mTLS listener and
  explicit advertised addresses. It is not exposed through worker or producer
  bearer tokens.
- Wi-Fi clients, laptops, shared-hosting accounts, and ordinary worker devices
  remain workers, not automatic voters.

## Snapshot, restore, and upgrade

Snapshots are versioned, checksummed representations of the deterministic FSM
state. Restore validates schema, cluster identity, checksum, maximum size, and
membership procedure before replacing local state. A restored snapshot never
silently adopts a foreign cluster identity.

Migration from standalone mode is explicit and one-way per attempt:

1. drain admission and take a verified backup;
2. convert the current state at one committed boundary into the initial FSM
   snapshot;
3. bootstrap one voter, add the remaining voters as staging/non-voters, then
   promote after catch-up;
4. switch the stable producer URL only after quorum and leader probes pass;
5. keep the backup offline; do not run the old standalone relay against the
   migrated store.

Rolling upgrades require an explicit command/snapshot compatibility matrix.
An older binary that cannot understand the committed command version refuses
to join or become writable.

## Why not HashiCorp Raft now

The first draft selected `hashicorp/raft` plus `raft-boltdb/v2`. That selection
was withdrawn before either dependency entered a release build. Upstream issue
[#695](https://github.com/hashicorp/raft/issues/695) reports reproduced log,
leader-completeness, state-machine, and election-safety violations caused by a
heartbeat fast path mutating Raft term/state concurrently with the main loop.
The report remains open and v1.8.0 does not claim a correction.

Disabling or locally patching a critical protocol path without an accepted
upstream contract would make CB the maintainer of an unverifiable fork. This
feature exists specifically to prevent divergent execution authority, so that
is not an acceptable release basis. HashiCorp Raft can be reconsidered only
after an upstream resolution and the same CB partition/replay harness passes
against an exact released version.

## Why not embedded JetStream now

[JetStream clustering](https://docs.nats.io/learn/topologies/jetstream-in-a-cluster)
also uses Raft groups and NATS can be embedded through its
[`server` package](https://pkg.go.dev/github.com/nats-io/nats-server/v2/server).
The NATS server is Apache-2.0 and publishes a larger dependency inventory.
It is an excellent event/stream platform, but it is not the narrower fit for
the first ContextBridge control-plane prototype:

- a work stream alone does not replicate CB token, pairing, idempotency,
  session-lock, policy, and receipt state;
- CB would still need a deterministic state machine spanning those records;
- embedding a general broker adds configuration, protocol, binary, security,
  and operational surface that the current single-binary core otherwise does
  not need;
- translating CB's ambiguous-execution rules into consumer redelivery defaults
  risks making queue convenience override correctness.

JetStream remains a valid future transport/event-plane option if independent
consumers, fan-out streams, or an existing NATS deployment become product
requirements. It is not rejected as inferior; it is deferred as broader than
the current consensus problem.

## Dependency and release gates

### Current dependency evaluation (2026-09-24)

`go.etcd.io/raft/v3` v3.7.0 resolves to upstream commit
`b867cf13f6bc0dae21204302df97bc2355c3af55`, requires Go 1.26, and is
Apache-2.0. It is the consensus core used by etcd and exposes deterministic
message/state transitions rather than owning CB's transport or disk. The exact
module is pinned in `go.mod` for the CB-specific partition safety test. It is
not imported by non-test code, linked into a release binary, or listed in the
runtime SBOM while the integration remains unsupported; its source and license
are nevertheless included in the corresponding-source vendor archive.

The earlier HashiCorp v1.8.0 / raft-boltdb v2.4.2 evaluation remains useful
historical evidence, but upstream issue #695 is now a rejection reason for
that backend rather than a reason to leave the entire HA design direction
undecided.

Before a Raft build can be called supported:

1. pin the exact module version and commit identity shown above;
2. record SPDX/license notices and obtain legal review for the AGPL/commercial
   distribution boundary (`etcd-io/raft` identifies as Apache-2.0; this
   document is not legal advice);
3. compare source archive, SBOM, binary size, idle RSS/CPU, open handles, and
   startup time against standalone mode;
4. run `govulncheck`, race tests, fuzz tests, static analysis, and deterministic
   release reproduction with the added dependency graph;
5. keep HA opt-in; standalone users do not pay the runtime or operational cost.

## Proof gates

The prototype remains unsupported until automation proves all of the following:

- repeated hard leader death under concurrent submit/cancel/start/result load;
- partitions where a minority never accepts a mutation;
- stale-leader dispatch and stale worker results rejected by fences;
- no duplicate assignment generation across the recorded log;
- each failover-boundary job is durably unstarted, completed once, or explicitly
  terminal/ambiguous—never silently replayed;
- idempotent retries return the original job after a leader change;
- snapshot/restore, member replacement, quorum loss, and rolling upgrade;
- 24-hour and 72-hour churn/soak tests with bounded disk, goroutines, handles,
  and memory.

Until those gates pass, documentation and probes continue to say
`mode: standalone`.
