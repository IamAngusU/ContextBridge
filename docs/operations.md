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

`console` is a view of the managed service. Leaving it with `exit` or Ctrl+C
does not stop the service. `contextbridge stop --config ./config.yml` asks a
loopback-managed `run`/`serve` process to stop and refuses while work is active.
`contextbridge stop --force --config ./config.yml` is the explicit interruption
override. Standalone relay/worker processes remain owned by their service
manager or foreground terminal.

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

See [security.md](security.md) for the threat model and
[limits-and-performance.md](limits-and-performance.md) for bounded payload and
measurement details.
