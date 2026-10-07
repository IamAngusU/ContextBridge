# Optional local outcome service for `do`

Operators can choose a local service implementing the Companion Agent v1
contract. This is separate from cluster scheduling and workspace adapter
authority. Existing installations retain their cluster default when unconfigured.

```yaml
agent_api:
  url: http://127.0.0.1:8770
  default_policy: offline
```

The URL must be a literal loopback HTTP(S) origin, without credentials, path,
query or fragment. The client does not inherit cluster secrets, use environment
proxies, follow redirects, discover/trust endpoints, start runtimes or fall back
to a remote model. The operator owns the local service and its policy.

`--mode daybreak-blue` is a separate capability request, not another network
policy. The local service must enable the mode, byte-admit its model/runtime,
and grant it separately to a remote device. ContextBridge merely forwards the
explicit value; it does not start a model or silently fall back to `standard`.

Ordinary foreground `do` preserves the built-in calculator/random-tool fast path
before contacting this service. For example `cb do "Was macht (4,2 * 10.1) mal 3 / 30 + 5?"`
returns 9.242 with the existing tool/verified-computation terminal lines, without
a model or job. Recognized invalid arithmetic fails closed. Compound prompts are
not reduced to embedded expressions. Explicit background, JSON, code, context and
job-identity requests retain their service contract. Human-readable service answers
show their actual method, source titles and review notice; model confidence is not
promoted to verification. Raw JSON output is unchanged.

```console
cb do "Explain additive colour mixing"
cb do --mode daybreak-blue --policy offline "Explain additive colour mixing"
cb do --policy offline --task code --background --timeout 900s "Create a small accessible HTML form"
cb do --policy offline --task code --design off --js-checks checks.json "Implement the specified function"
cb jobs list
cb jobs show latest
cb jobs --output receipt.json show latest
cb jobs cancel latest
cb jobs resume latest
cb jobs versions latest
cb jobs --version 1 --output candidate-v1.json show latest
cb jobs --version 1 resume latest
cb jobs diff latest
cb jobs --base previous --patch diff latest
cb jobs --version 2 --base 1 --path src/app.js --patch diff latest
cb jobs --output diff.json diff latest
cb jobs activity latest
cb do --task code --policy offline --workspace-context context.json "Repair the selected implementation"
cb do --task code --policy offline --rules engineering-defaults "Draft a cache-versioned page"
cb do --task diagnose --policy offline "Which process listens on port 8770?"
cb do --task diagnose --policy offline --diagnostic-workspace demo "Inspect project status"
cb tools list
cb tools --set port=8770 call network.listeners
```

Flags precede positional arguments. `--use` selects an explicit component
reference; `--design` selects a design card/default/off. `--research` is an
explicit public query allowed only by the selected policy. `--json` prints the
complete foreground receipt. `--request-id` supplies a stable idempotency key
for an intentional retry; the client never automatically replays submissions.

Compatible services accept `--rules ID[@revision]` (repeatable for code),
`default`, or `off`. These are pinned preferences, not execution authority.
`diagnose` explicitly uses offline-only read-only host diagnostics. An optional
`--diagnostic-workspace ID` selects a scope already registered by its operator;
it is never a filesystem root/path grant. The service defines its bounded tool
allowlist and returns observations, not permission to stop a PID. Tools are
discovered through GET `/api/agent/tools` and called via POST
`/api/agent/tools/call` with `{name,arguments}`. `cb tools --arguments FILE call NAME`
reads strict JSON (256 KiB maximum). These routes use the configured loopback
agent service only, not cluster credentials or an inherited cloud provider.
Unknown tools/arguments and older unsupported services fail without fallback.
For small fields use repeated `--set FIELD=VALUE`: JSON values are typed and
other values remain strings. Duplicate fields and mixing input mechanisms are
rejected. This avoids JSON quote loss in Windows `.cmd` wrappers. Use an explicit
JSON arguments file for complex/nested data or text that itself resembles JSON.

`--workspace-context` reads an explicit prepared `contextbridge.workspace-context.v1`
JSON file, bounded to 640 KiB. Duplicate keys are rejected. The client refuses
hybrid inference unless the snapshot explicitly permits cloud use; the service
also enforces its own policies, path scope and source hashes. A capability check
rejects an older service before source submission. This is not a host directory
traversal or workspace write grant: the result remains a reviewable proposal and
existing workspace adapters retain their independent authority and CAS checks.
The service may persist selected source in its private job journal; operators
must understand its retention policy before supplying private files.

Policies: local tools only (`local`), local inference without web (`offline`),
local inference plus configured web (`local-agent`), or permitted local-first
cloud fallback (`hybrid`). Remote permission never overrides the service's
global cloud prohibition. Pure-JS checks require the service's bounded no-host-I/O
checker; these are not shell execution or arbitrary build permission.

An explicit provider/model/profile/account selects the legacy cluster route.
For interactive cluster chat, use `cb cluster chat`; the local outcome command
requires a task. `do` submits `/api/agent/jobs`, polls its owner-scoped receipt,
and reports failures rather than treating a failed verification as success.
Foreground Ctrl+C requests cancellation of exactly the acknowledged job, while
`--background` deliberately leaves it running. No job is replayed after a lost
acknowledgement. Retention/restart/retry budgets are service-owned and discoverable
through `/api/agent/capabilities` and `/api/agent/jobs/schema`.

Services implementing code revisions also expose `jobs versions`, `show --version`
and explicit `resume --version` (place flags before the subcommand as above).
Committed complete candidates survive interrupted checks; partial provider tokens
are not a committed candidate. A service may reuse an exact previously verified
code candidate for the same owner, sources, task, tests and policy, and recheck it
locally. `do --fresh` disables that optimization, not the provider's own cache.

Compatible services expose read-only `/api/agent/jobs/{id}/diff` with `version`
(`latest` or a stored version), `base` (`original`, `previous`, earlier version),
optional exact relative `path` and `include_patch`. Responses distinguish pinned
proposal deltas from actual disk writes. `/activity` returns a compact job/diff
snapshot; `/activity/stream` emits `activity.snapshot` SSE frames, replacing prior
state. Reconnect reads current state, not an event replay; there is no cursor.
These are not the cluster lifecycle event stream or workspace apply receipts.
An older service returns an error; the CLI never infers missing counts as zero.

`jobs` uses list/get/versions/diff/activity/cancel/resume endpoints. `latest` is resolved by the service
within the caller's ownership boundary. Saving a receipt is create-only and does
not execute the code. Private licensed phone relays are service deployments, not
public Core credentials or a public proxy for loopback admin APIs.
