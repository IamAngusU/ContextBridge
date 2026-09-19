# Security model

ContextBridge is local-first. Its default listener is `127.0.0.1`, its token is generated from 32 random bytes, saved files use owner-only permissions where the operating system supports them, and API responses are marked `no-store`.

The optional OpenAI-compatible ingress uses that same authenticated loopback
listener; it is not an unauthenticated second server. It rejects remote image
fetches, tool calls, oversized history, and unknown route-backed model IDs. A
configured OpenAI-compatible output engine cannot send prompts to a remote
host unless the URL is HTTPS, `remote: true` is explicit, and its API key has
resolved. Provider secrets are omitted from public config/status JSON.

Portable-resource discovery reads only a strict, size-bounded marker at a
volume root or direct child. It neither follows linked markers nor recursively
indexes or executes removable media. Portable manifests may publish loopback
HTTP endpoints only. Presence means “detected”, not “trusted to run”; runtime
startup remains an explicit operator action.

The optional MCP adapter is stdio-only and connects back to that authenticated
loopback listener. It never prints the bearer token, exposes no arbitrary shell
or MCP client, accepts only a closed metadata allowlist, rejects artifact jobs
before submission, removes endpoint URLs and credential-shaped status fields,
and never retries a state-changing submit. The MCP host inherits the authority
of the config it can read, and receives the remaining operational metadata or
result it requests; stdio does not create a separate tenant boundary. See [MCP
stdio server](mcp.md).

The cluster relay also binds to localhost by default and must be published through HTTPS. Workers connect outward using `wss://`. Browser WebSocket origins are rejected unless explicitly listed. Pairing endpoints are rate limited, pairing codes expire, and a node token is returned only once. Admin, observer, producer, and node credentials have separate roles.

Normal cluster jobs are encrypted in transit by TLS but remain readable by the relay. Optional E2EE jobs use a two-phase reservation. The producer receives one worker's X25519 public key, encrypts the payload with an ephemeral X25519 key and AES-256-GCM, and authenticates the job ID, selected node, assignment attempt, authenticated producer subject, tenant, session, and complete worker requirements as additional data. The worker reconstructs that context from the outer job and fails before local execution if any field changed. It encrypts the result under the same context with a separately derived response key. The relay stores and forwards ciphertext only.

E2EE does not hide routing metadata such as task, group, model requirement, priority, timestamps, payload size, or selected node. ContextBridge does not transparently retry either plaintext or E2EE work after a worker error, disconnect, or execution timeout: the browser or model may already have accepted the request, so repeating it could duplicate side effects. Such a job fails closed and requires an explicit new submission. An E2EE resubmission additionally requires a new reservation because another worker has another private key.

Terminal chat supports E2EE per session with `--e2ee` or interactively with `/e2ee on`; `/e2ee off` returns to TLS-only relay-visible jobs. Plaintext progress streaming is disabled while E2EE is active. Encryption overhead is small compared with model inference, but worker reservation, loss of live plaintext progress, and loss of transparent failover are meaningful operational tradeoffs.

The browser extension does not receive permanent access to every page. The operator explicitly attaches a tab and approves that tab's origin; highlighting a tab is not authorization. Existing conversations require explicit attachment. Optional auto-attachment applies only to confirmed empty new ChatGPT/Gemini chats on previously permitted origins and can be disabled. A tab can be detached without stopping the whole bridge; already-submitted website work may finish, but no new job is sent there. The visual profile contains CSS selectors, not executable JavaScript. The extension sends only the configured prompt and optional image, then reads the configured response elements. Optional all-tabs access is requested when the operator browses every window or enables the fresh-tab helper.

Browser conversation ownership remains local. For cluster affinity, the
extension may advertise only a fixed-format SHA-256 selector derived by the
authenticated worker for a binding whose permanent URL still matches. It never
advertises the URL, public `session_id`, prompt, response, draft, or title. The
relay validates and uses that selector internally, fails closed on duplicates,
and redacts it from node-list API responses. The extension still proves the
exact saved URL immediately before sending; the selector alone grants no page
access and cannot bypass origin permission, the 16-tab bound, or an occupied
conversation.

Optional draft preservation is disabled by default. When enabled, the extension saves an unsent text draft to `~/.contextbridge/draft-history.jsonl` through the authenticated localhost service before clearing that unchanged editor. The file is bounded to 1 MiB and inherits the user-home directory's access controls; it is plaintext local history, **not** E2EE-protected and never sent to the relay. A shared OS account should keep the option off for sensitive drafts. Missing private-home storage or an editor change aborts the job without clearing the draft.

Submitted text and image text are treated as untrusted data. ContextBridge wraps them in trusted instructions that explicitly forbid following embedded commands. Model output is parsed as JSON and reduced to the configured decision vocabulary. Invalid, missing, or timed-out output becomes `review`.

Prompt injection cannot be solved by one instruction. ContextBridge therefore uses multiple boundaries: producer authentication, task and group allowlists, bounded input and output sizes, fixed provider routes, no shell evaluation, JSON validation, tenant separation, bounded pipeline loops, and explicit human approval where a workflow requires it. Applications must still choose conservative output contracts for high-impact actions.

Managed model and runtime downloads have two independent integrity boundaries. `llama.cpp` archives are accepted only with the SHA256 digest published on the official GitHub release asset. GGUF files are checked against Hugging Face LFS SHA256 metadata or an explicit manifest digest. Archive entries are path-normalized before extraction.

ContextBridge application updates are handled by the Go core, not by dashboard JavaScript. Stable release archives must match both the release `SHA256SUMS` entry and the GitHub asset digest. The staged executable must start and report the expected version before it replaces the current executable. Replacement is atomic where the operating system permits it, and the previous executable remains available for rollback. A process-wide mutex and an owner-only cross-process lock prevent the service and scheduled updater from replacing the binary concurrently.

The hosted installer endpoint records aggregate request counts and a daily rotating HMAC fingerprint derived from a masked network prefix. User-agent variants are not part of uniqueness; per-network daily admission, per-day cardinality, and a 31-day window bound synchronous abuse and stored rows. It does not store raw IP addresses, cookies, prompts, results, node identities, or model activity. This counter belongs to the hosted website only and is not part of the local runtime or self-hosted relay.

Relay detail retention is mandatory and bounded by administrator-configured age and count limits. It applies to exact terminal jobs (`completed`, `failed`, or `cancelled`), events, exact terminal pipeline runs, and the separate pseudonymous session-placement cache; queued, reserved, assigned, running, and unknown states are never deleted by the sweep. Job payloads, results, sealed ciphertext, and their matching detail indexes are removed atomically. Opaque placement hashes contain no public session name or conversation URL and are independently removed when old or over their configured count. Lifetime state and usage summaries are retained without prompt/result contents, while cumulative node compute and cost metrics remain unchanged. This reduces indefinite sensitive-history retention and lets BoltDB reuse freed pages, but it is not secure erasure from storage media and does not immediately compact the database file.

Engine processes receive explicit argument arrays without a shell. Model output cannot alter process arguments, routes, executable paths, or GPU policy. Managed engines bind to localhost.

Do not expose the local port directly to a network. Use an authenticated tunnel or a TLS reverse proxy with an additional access policy when a remote source must reach ContextBridge. Keep the YAML token and browser extension pairing private.

Browser interfaces change. A selector profile can stop working after a site update. This fails to `review`; it must never silently become `allow`.

Extension packages contain no remotely hosted code. Chromium and Firefox builds share reviewed source but use browser-specific manifests. Their content security policies allow the configured localhost connection and HTTPS resource reads after the relevant page permission is granted, without granting remote script sources.

The dashboard receives its token through manual entry or a URL fragment opened by the CLI. URL fragments are not sent in HTTP requests. The dashboard removes the fragment and keeps the token in session storage, so closing the tab ends that dashboard session.

Job IDs are validated before they become file names and are sanitized again at the storage boundary. Job files are created atomically, so duplicate IDs cannot replace an existing request. Browser leases expire, reject late completions, and are renewed by an active worker while a page response is still running.

The local RAG backend partitions records by `tenant_id`. Applications must authenticate users and derive tenant IDs server-side rather than accepting an arbitrary tenant from an untrusted browser. For large or replicated indexes, use a dedicated vector database adapter with its own authorization boundary.

The relay database and worker identity file contain sensitive material. The database contains token hashes and short-lived pairing delivery state. The identity file contains a node token and X25519 private key. Keep both owner-only, exclude them from backups shared with other tenants, and rotate pairing by removing the identity and pairing again if a device is lost.

## Privacy and compliance boundary

These mechanisms can support an operator's security and privacy program, but
they do not make every deployment legally compliant or audited. Ordinary relay
jobs are visible to the relay, browser providers receive content submitted to
their pages, several local stores need an operator-defined lifecycle, and SOC 2
requires independent organizational evidence over a defined scope and period.
See [Privacy and compliance readiness](compliance-readiness.md) for the data
inventory, current retention boundaries, DSAR runbook, recipient register, and
readiness mapping. The MIT software license is separate from compliance and
attestation.
