# Operations

This guide covers the provider-neutral public core. Commands use an explicit
configuration path so they behave the same from a workstation, VPS, CI job, or
service account.

## Install and verify

Windows PowerShell:

```powershell
irm https://raw.githubusercontent.com/IamAngusU/ContextBridge/main/install.ps1 | iex
```

Linux or macOS:

```sh
curl -fsSL https://raw.githubusercontent.com/IamAngusU/ContextBridge/main/install.sh | sh
```

Open a new shell after installation, then run:

```sh
contextbridge version
contextbridge doctor --config ./config.yml
contextbridge selftest --config ./config.yml
```

`selftest` is readiness-only by default. Add `--run` only when a real bounded
model request is intended.

## Run one local service

```sh
contextbridge run --config ./config.yml
```

In another shell:

```sh
contextbridge status --config ./config.yml
contextbridge cluster chat \
  --config ./config.yml \
  --provider ollama \
  --model auto \
  --artifacts off \
  --prompt "Reply exactly with LOCAL-ROUTE-OK"
```

On Windows CMD, place a multi-line shell example on one line or replace each
trailing backslash with `^`. PowerShell uses a backtick.

## Build a pool

Run the relay on the coordination host:

```sh
contextbridge cluster configure --config ./config.yml --mode relay --listen auto
contextbridge run --config ./config.yml
```

Join a worker from another machine. The worker dials out; it does not require
an inbound public worker port.

```sh
contextbridge cluster configure \
  --config ./config.yml \
  --mode worker \
  --relay-url https://relay.example.net \
  --name gpu-workstation
contextbridge pair --config ./config.yml --name gpu-workstation
contextbridge run --config ./config.yml
```

The role can be changed without reinstalling. A sender/client keeps its relay
address and producer credential but contributes no worker capacity:

```sh
contextbridge stop --config ./config.yml
contextbridge cluster configure --config ./config.yml --mode client --relay-url https://relay.example.net
contextbridge run --config ./config.yml
```

`--mode sender` is an alias for `--mode client`. The worker identity is retained
for a later switch back; the new role takes effect after the service restart.
Switch back with `--mode worker`, run `doctor`, and pair again only when doctor
reports that the retained identity is missing or belongs to another relay.
Changing the role does not revoke a producer credential. Revoke that scoped
token at the relay when the device must no longer be allowed to submit work.

Inspect before submitting work:

```sh
contextbridge cluster status --config ./config.yml
contextbridge route explain --config ./config.yml --file ./examples/cluster-job.json
contextbridge cluster submit --config ./config.yml --file ./examples/cluster-job.json
```

## Observe, diagnose, and stop

```sh
contextbridge console --config ./config.yml
contextbridge dashboard --config ./config.yml
contextbridge doctor --config ./config.yml
contextbridge hardware --config ./config.yml
contextbridge models --config ./config.yml
contextbridge resources --config ./config.yml
contextbridge benchmark
```

Prometheus-compatible pool aggregates are available to relay administrators
and observer tokens at `GET /metrics`. They intentionally omit user-controlled
and tenant-specific labels:

```sh
curl -fsS -H "Authorization: Bearer $CONTEXTBRIDGE_TOKEN" https://relay.example.net/metrics
```

`console` is a detachable view and, when a scoped producer credential is
available, a bounded interactive job client. Leaving it with `exit` or Ctrl+C
does not stop the service. `contextbridge stop --config ./config.yml` asks a
loopback-managed `run`/`serve` process to stop and refuses while work is active.
`contextbridge stop --force --config ./config.yml` is the explicit interruption
override. Standalone relay/worker processes remain owned by their service
manager or foreground terminal.

The command row is never a PowerShell/CMD/shell surface. Type `help` there to
see the complete bounded vocabulary. `details show 1`, `gpus show all`, and
`models hide 2` set per-node detail rows explicitly; `details none` collapses
every visible node.

Work actions are enabled only in an interactive terminal when a credential is
resolved from `console --token`, `CONTEXTBRIDGE_CLUSTER_TOKEN`, or
`cluster.client_token`, in that order. The console deliberately does not inherit
`cluster.relay.admin_token`. Without a producer credential it remains read-only
and explains why. Piped or redirected console input also remains read-only.

```text
cb › send Summarize why durable idempotency matters in two sentences.
cb › send --provider ollama --model auto --egress local_only -- Summarize this locally.
cb › jobs
cb › job job_...
cb › result job_...
cb › cancel job_...
```

The composer uses one structured grammar, not shell parsing. `send TEXT` is the
short path. `send [FLAGS] -- TEXT` enables bounded routing flags:
`--provider`, `--model`, `--group`, `--session`, `--profile`, `--reasoning`,
`--egress`, `--max-cost-usd`, `--new-session`, and
`--new-session-per-job`. Provider-specific flags disappear from the suggested
next set when they cannot apply. The displayed `[Length X · allowed 1–Y]`
bound comes from `terminal.max_prompt_characters`; `[Payload X/Y bytes]` uses
the authenticated relay protocol manifest and accounts for the prompt's exact
JSON/UTF-8 encoding. Yellow means incomplete, red means rejected, and ordinary
terminal text means the current input is ready. The relay validates the final
request again and remains authoritative.

The compact service flags are configuration/runtime evidence, not inferred
marketing claims: `RLY` relay, `WRK` worker, `UPD` verified updates, `RAG`
retrieval, `PCK` portable resource-pack discovery, and `EAS` at least one
configured engine with autostart. Green is enabled; dim grey is disabled.

The local character guard defaults to 4096 and can be changed without
recompiling:

```yaml
terminal:
  max_prompt_characters: 4096
```

Values from 64 through 65536 are accepted. This guard cannot widen a stricter
relay byte limit.

`send` creates one bounded plaintext text job through the normal relay admission
path. Relay policy, ownership, queue/rate limits, egress rules and cost policy
apply exactly as for another producer. The client follows only authoritative
relay lifecycle events, keeps at most four automatic followers, and bounds text
shown in the panel without altering the retained result. A lost plaintext submit
response is retried once with the same idempotency key, so it still represents
one logical job. Cancellation makes relay state terminal but does not prove that
a side-effecting provider execution instantly stopped.

Use `cluster chat --e2ee` for encrypted prompts and results. The initial console
composer is intentionally plaintext-only and never silently downgrades an E2EE
request. Persistent configuration, token management, pairing, updates,
installation, node administration, arbitrary URLs/files and host commands stay
outside the console action vocabulary.

## Remove ContextBridge safely

Preview the exact removal plan first:

```sh
contextbridge uninstall --config ./config.yml --dry-run
```

An ordinary uninstall removes only installer-owned program files and
integrations, including the owned launcher, autostart entry, PATH entry,
shortcut, and shell completion. Configuration and managed data remain in
place:

```sh
contextbridge uninstall --config ./config.yml
```

Use `--purge` only when the locally managed configuration, credentials, job
state, models, and runtime data should also be removed:

```sh
contextbridge uninstall --config ./config.yml --purge
```

The purge plan follows only paths that remain inside the installation or
configuration directory after resolving existing symlinks. External artifact
directories, global Ollama data, portable resource packs, external secret
files, and other operator-owned paths are reported as preserved rather than
deleted. A malformed configuration stops the purge; `--force` removes only
the bounded paths that can still be proven local and reports that external
configured paths could not be discovered. It does not turn an unknown path
into an owned path.

Installers also write a bounded `.contextbridge-install.json` ownership
manifest. It contains only relative program paths and lets optional packages
register their own installed directories without teaching the public core what
those packages contain. The uninstaller rejects malformed manifests, path
escapes, symlinks, duplicate entries, and attempts to claim mutable
configuration or data. Older installations without the manifest retain the
conservative built-in program list; running the current installer once creates
the manifest.

Both modes refuse to interrupt active work unless `--force` is explicit.
Redirected or automated use also requires `--yes`; an interactive purge
requires typing `PURGE`. When automatic installation discovery is impossible,
`--install-dir` must point to a directory containing both the ContextBridge
binary and its installer marker file.

Windows performs deletion through a detached helper after the running binary
exits. It verifies files, launchers, PATH, owned scheduled tasks, completion
markers, and owned Start Menu shortcuts. A clean uninstall leaves no report;
if an owned item remains, the helper preserves its plan and writes the exact
remainder to the verification-log path printed by the command.

### Fleet decommissioning boundary

`uninstall` acts on the machine that runs it. The current protocol deliberately
does not turn `--node` or `--all` into remote deletion: a disconnected worker,
an acknowledgement lost while its relay is being removed, or a combined
relay-and-worker process would otherwise make “removed everywhere” impossible
to prove.

A future fleet operation must be a separate decommission-marker protocol, not
an alias for this command. Its approval must bind an exact sorted node-ID set,
pool snapshot, purge mode, and expiry; every worker must opt in locally, drain
admission, acknowledge the marker, and report an independent terminal result.
Offline, ambiguous, newly joined, and non-opted-in nodes must remain explicit
failures. The relay itself must be decommissioned last through its local
operator path. Until those invariants are implemented and tested, run the
local dry-run and uninstall on each intended machine.

## Operational invariants

- A job is not replayed after ambiguous execution merely because a connection
  disappeared.
- Unknown capability or cost stays unknown; policy decides whether to reject
  it.
- A worker assignment is lease-bound and result ownership is verified.
- Artifacts are revalidated before saving or passing them to another step.
- Schedules persist claims before dispatch and do not create an unbounded
  catch-up storm after downtime.
- Update staging is not reported as installation; the new executable must
  actually become the running healthy version.
- Removal acts only on installer-owned integrations and proven managed paths;
  ambiguous or external ownership is preserved.

See [security.md](security.md) for the threat model and
[limits-and-performance.md](limits-and-performance.md) for bounded payload and
measurement details.
