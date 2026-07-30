# Security model

ContextBridge is local-first. Its default listener is `127.0.0.1`, its token is generated from 32 random bytes, saved files use owner-only permissions where the operating system supports them, and API responses are marked `no-store`.

The browser extension does not receive permanent access to every page. The operator chooses a tab, approves that tab's origin, and teaches or selects one browser profile. The visual profile contains CSS selectors, not executable JavaScript. The extension sends only the configured prompt and optional image, then reads the configured response elements. Optional all-tabs access is requested only when the operator chooses to browse tabs from every window.

Submitted text and image text are treated as untrusted data. ContextBridge wraps them in trusted instructions that explicitly forbid following embedded commands. Model output is parsed as JSON and reduced to the configured decision vocabulary. Invalid, missing, or timed-out output becomes `review`.

Managed model and runtime downloads have two independent integrity boundaries. `llama.cpp` archives are accepted only with the SHA256 digest published on the official GitHub release asset. GGUF files are checked against Hugging Face LFS SHA256 metadata or an explicit manifest digest. Archive entries are path-normalized before extraction.

Engine processes receive explicit argument arrays without a shell. Model output cannot alter process arguments, routes, executable paths, or GPU policy. Managed engines bind to localhost.

Do not expose the local port directly to a network. Use an authenticated tunnel or a TLS reverse proxy with an additional access policy when a remote source must reach ContextBridge. Keep the YAML token and browser extension pairing private.

Browser interfaces change. A selector profile can stop working after a site update. This fails to `review`; it must never silently become `allow`.

Extension packages contain no remotely hosted code. Chromium and Firefox builds share reviewed source but use browser-specific manifests. The Firefox content security policy explicitly allows the configured localhost connection without granting additional remote script sources.

The dashboard receives its token through manual entry or a URL fragment opened by the CLI. URL fragments are not sent in HTTP requests. The dashboard removes the fragment and keeps the token in session storage, so closing the tab ends that dashboard session.

Job IDs are validated before they become file names and are sanitized again at the storage boundary. Job files are created atomically, so duplicate IDs cannot replace an existing request. Browser leases expire, reject late completions, and are renewed by an active worker while a page response is still running.

The local RAG backend partitions records by `tenant_id`. Applications must authenticate users and derive tenant IDs server-side rather than accepting an arbitrary tenant from an untrusted browser. For large or replicated indexes, use a dedicated vector database adapter with its own authorization boundary.
