# ContextBridge

Own the compute. Route the work.

ContextBridge is a local-first execution fabric for AI workloads. It turns the
models, APIs, computers, GPUs, removable resource packs, and servers you control
into one policy-aware pool that can be used from a terminal, an application, a
VPS, shared hosting, or an MCP client.

You describe the job and its hard requirements. ContextBridge selects a
compatible worker, reserves capacity, enforces egress and cost policy, verifies
the result, and records why the route was chosen.

## Why it exists

AI infrastructure is usually fragmented: one machine has a GPU, another hosts
an API credential, a VPS receives requests, and a portable drive contains
specialized models. ContextBridge joins those resources without opening inbound
ports on worker machines or pretending an unavailable capability exists.

Its design rule is simple:

> A component saying that something happened is not proof that it happened.

That rule is applied to queue state, worker leases, updates, artifacts, model
capabilities, costs, agent approvals, and execution receipts.

## Highlights

- Local Ollama and managed `llama.cpp` engines.
- OpenAI-compatible API engines with operator-owned credentials and budgets.
- N:1 and N:N worker pools over an authenticated relay.
- Capability-aware scheduling for text, vision, images, audio, files,
  embeddings, RAM, VRAM, tags, models, and worker groups.
- End-to-end encryption between a producer and one reserved worker.
- Verified artifact transfer with byte limits, media validation, SHA-256, and
  non-overwriting saves.
- Durable queueing, cancellation, idempotency, execution receipts, schedules,
  and bounded multi-step agents.
- Named trust policies so operators decide which low-risk work may run without
  a per-run approval and which work must stop for review.
- Optional out-of-tree adapters through a provider-neutral endpoint contract.
  The core contains no vendor-specific adapter implementation.
- MCP, a dependency-free PHP client example, a JSON job API, and an
  OpenAI-compatible input surface.
- Fail-closed routing: unknown capability, unknown price, ambiguous ownership,
  or lost execution state is not silently treated as success or zero cost.

## Five-minute start

### Windows PowerShell

```powershell
irm https://raw.githubusercontent.com/IamAngusU/ContextBridge/main/install.ps1 | iex
```

### Linux or macOS

```sh
curl -fsSL https://raw.githubusercontent.com/IamAngusU/ContextBridge/main/install.sh | sh
```

The installer verifies the release checksum, creates a private local config,
offers Ollama or a managed runtime, configures the requested relay/worker role,
and installs shell completion. It never replaces an unrelated `contextbridge`
or `cb` command. If both names are occupied, choose a custom command name.

Start the local service:

```sh
contextbridge run --config ./config.yml
```

Check it:

```sh
contextbridge doctor --config ./config.yml
contextbridge status --config ./config.yml
contextbridge selftest --config ./config.yml
```

The default self-test performs readiness checks only. It sends no model prompt
unless `--run` is supplied.

## First local job

```sh
contextbridge cluster chat \
  --config ./config.yml \
  --provider ollama \
  --model auto \
  --artifacts off \
  --prompt "Reply exactly with LOCAL-ROUTE-OK"
```

For Windows CMD, put the command on one line or use `^` for continuation.
PowerShell uses a backtick. A Bash backslash is not a Windows continuation.

## Build a pool

Run a relay on a server and join workers from private machines. Workers dial
out to the relay; no inbound worker port is required.

```sh
# Relay
contextbridge cluster configure --config ./config.yml --mode relay --listen auto

# Worker
contextbridge cluster configure \
  --config ./config.yml \
  --mode worker \
  --relay-url https://relay.example.net \
  --name gpu-workstation
contextbridge pair --config ./config.yml --name gpu-workstation
contextbridge run --config ./config.yml
```

From the relay or an authenticated producer:

```sh
contextbridge cluster status --config ./config.yml
contextbridge route explain --file ./examples/cluster-job.json
contextbridge cluster submit --config ./config.yml --file ./examples/cluster-job.json
```

The scheduler matches hard requirements first, then ranks compatible nodes by
live capacity and resource evidence. An assigned or running job is never
silently replayed on another worker after an ambiguous disconnect.

## “Take care of it” without surrendering control

`cluster agent plan` creates a bounded JSON plan. `cluster agent run` normally
requires approval of the exact plan and execution fingerprint. For trusted
projects, an operator may define a named authority that permits only a bounded
set of providers, profiles, tenants, groups, egress modes, budgets, steps, and
runtime. The planner cannot grant or widen its own authority.

```sh
contextbridge cluster agent plan \
  --config ./config.yml \
  --goal "Draft a concise release note and verify it" \
  --out plan.json

contextbridge cluster agent run \
  --config ./config.yml \
  --plan plan.json \
  --approve sha256:REVIEWED_HASH
```

If execution-relevant configuration changes after approval, the run stops and
requires a new review. Purely operational settings are separated from the
execution fingerprint where they cannot change model behavior.

## Use it from applications

- Native JSON jobs: authenticated HTTP queue API.
- OpenAI-compatible input: for existing clients that can change a base URL.
- MCP: bounded tools for job submission, status, results, and cancellation.
- PHP 8.1+: dependency-free example for shared hosting.
- Folder inbox: atomic file-based ingestion for simple local automation.

See [docs/architecture.md](docs/architecture.md),
[docs/security.md](docs/security.md), [docs/supply-chain.md](docs/supply-chain.md),
[docs/adapters.md](docs/adapters.md), and the
[Hosted Relay readiness boundary](docs/hosted-relay.md).

## Honest boundaries

- ContextBridge is an orchestrator, not a model. Output quality remains the
  selected model's responsibility.
- `model: auto` means smallest compatible available model, preferring an
  already loaded one. It does not guess intelligence from a model name.
- Hardware telemetry is advisory unless the job declares a hard minimum.
- Unknown remote cost is unknown, not `$0`. A policy may reject it.
- Multi-GPU telemetry and placement are supported, but ContextBridge does not
  split one inference across GPUs unless the selected engine does so.
- Adapters are separate executables owned and configured by the operator. The
  public core neither ships nor documents vendor-specific adapters.
- The project does not claim GDPR certification, SOC 2 attestation, or a legal
  compliance guarantee. It provides controls and evidence that can support an
  operator's own compliance program.
- The repository contains a relay foundation, not a currently offered
  production multi-tenant Hosted Relay, SLA, or zero-knowledge service.

## Security defaults

- Local service binds to loopback by default.
- Relay and worker roles use separate scoped credentials.
- Worker pairing is explicit and auditable.
- Secrets are not included in status output, receipts, or execution hashes.
- Artifact paths, sizes, digests, and media types are verified.
- Model/runtime downloads are bounded and revision-aware.
- Updates are opt-in, checksum-verified, staged, and considered installed only
  when the new executable is actually running and healthy.
- Adapter endpoints are untrusted capability reporters; scheduling uses bounded
  normalized evidence and exact lease ownership.
- Release archives are deterministic from a clean commit, contain a CycloneDX
  SBOM with reviewed runtime-license metadata, bundled third-party notices, and
  an explicit corresponding-source offer. Every AGPL binary release also
  publishes a deterministic source archive with vendored Go module sources.
  The machine-readable build record binds that archive to the exact commit.
  That record is currently unsigned and is not represented as proof of
  publisher identity.

Report vulnerabilities privately as described in [SECURITY.md](SECURITY.md).

## Development

```sh
go test ./...
go vet ./...
go test -race ./internal/bridge ./internal/cluster
```

The repository targets Windows, Linux, and macOS. CI builds and tests all three;
GPU/runtime availability naturally varies by host.

## License

The v0.7 development-line core is AGPL-3.0-only. Reusable schemas, examples,
and the adapter contract are Apache-2.0 exceptions with explicit directory or
SPDX notices. Published v0.6.x releases remain MIT; their permissions are not
withdrawn. See [LICENSING.md](LICENSING.md) for the exact file boundaries.

The code licenses do not grant permission to use the ContextBridge name or
visual identity in a way that implies an unofficial fork, service, or product
is maintained or endorsed by this project. Accurate origin statements such as
“based on ContextBridge” remain welcome; see
[TRADEMARKS.md](TRADEMARKS.md).
