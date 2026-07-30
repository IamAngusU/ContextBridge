# Security model

ContextBridge is local-first. Its default listener is `127.0.0.1`, its token is generated from 32 random bytes, saved files use owner-only permissions where the operating system supports them, and API responses are marked `no-store`.

The cluster relay also binds to localhost by default and must be published through HTTPS. Workers connect outward using `wss://`. Browser WebSocket origins are rejected unless explicitly listed. Pairing endpoints are rate limited, pairing codes expire, and a node token is returned only once. Admin, observer, producer, and node credentials have separate roles.

Normal cluster jobs are encrypted in transit by TLS but remain readable by the relay so they can be requeued to a different compatible worker. Optional E2EE jobs use a two-phase reservation. The producer receives one worker's X25519 public key, encrypts the payload with an ephemeral X25519 key and AES-256-GCM, and authenticates the job ID and node ID as additional data. The worker encrypts the result with a separately derived response key. The relay stores and forwards ciphertext only.

E2EE does not hide routing metadata such as task, group, model requirement, priority, timestamps, payload size, or selected node. It also prevents transparent failover because another worker has another private key. This is a deliberate security and availability tradeoff.

The browser extension does not receive permanent access to every page. The operator chooses a tab, approves that tab's origin, and teaches or selects one browser profile. The visual profile contains CSS selectors, not executable JavaScript. The extension sends only the configured prompt and optional image, then reads the configured response elements. Optional all-tabs access is requested only when the operator chooses to browse tabs from every window.

Submitted text and image text are treated as untrusted data. ContextBridge wraps them in trusted instructions that explicitly forbid following embedded commands. Model output is parsed as JSON and reduced to the configured decision vocabulary. Invalid, missing, or timed-out output becomes `review`.

Prompt injection cannot be solved by one instruction. ContextBridge therefore uses multiple boundaries: producer authentication, task and group allowlists, bounded input and output sizes, fixed provider routes, no shell evaluation, JSON validation, tenant separation, bounded pipeline loops, and explicit human approval where a workflow requires it. Applications must still choose conservative output contracts for high-impact actions.

Managed model and runtime downloads have two independent integrity boundaries. `llama.cpp` archives are accepted only with the SHA256 digest published on the official GitHub release asset. GGUF files are checked against Hugging Face LFS SHA256 metadata or an explicit manifest digest. Archive entries are path-normalized before extraction.

Engine processes receive explicit argument arrays without a shell. Model output cannot alter process arguments, routes, executable paths, or GPU policy. Managed engines bind to localhost.

Do not expose the local port directly to a network. Use an authenticated tunnel or a TLS reverse proxy with an additional access policy when a remote source must reach ContextBridge. Keep the YAML token and browser extension pairing private.

Browser interfaces change. A selector profile can stop working after a site update. This fails to `review`; it must never silently become `allow`.

Extension packages contain no remotely hosted code. Chromium and Firefox builds share reviewed source but use browser-specific manifests. The Firefox content security policy explicitly allows the configured localhost connection without granting additional remote script sources.

The dashboard receives its token through manual entry or a URL fragment opened by the CLI. URL fragments are not sent in HTTP requests. The dashboard removes the fragment and keeps the token in session storage, so closing the tab ends that dashboard session.

Job IDs are validated before they become file names and are sanitized again at the storage boundary. Job files are created atomically, so duplicate IDs cannot replace an existing request. Browser leases expire, reject late completions, and are renewed by an active worker while a page response is still running.

The local RAG backend partitions records by `tenant_id`. Applications must authenticate users and derive tenant IDs server-side rather than accepting an arbitrary tenant from an untrusted browser. For large or replicated indexes, use a dedicated vector database adapter with its own authorization boundary.

The relay database and worker identity file contain sensitive material. The database contains token hashes and short-lived pairing delivery state. The identity file contains a node token and X25519 private key. Keep both owner-only, exclude them from backups shared with other tenants, and rotate pairing by removing the identity and pairing again if a device is lost.
