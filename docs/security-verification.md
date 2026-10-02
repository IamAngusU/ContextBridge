# Security verification

ContextBridge uses complementary checks rather than treating one scanner or
badge as proof of security. All automated results are bounded engineering
evidence, not a certification or a substitute for deployment-specific review.

## Continuous gates

The public CI runs:

- ordinary tests, `go vet`, and race detection on concurrency-sensitive code;
- `govulncheck` for reachable Go vulnerability analysis and Dependabot for
  dependency and GitHub Actions advisories;
- `gosec` plus CodeQL `security-extended` queries as independent Go static
  analysis engines; CodeQL also analyzes the public JavaScript surface;
- Gitleaks over complete Git history, with only exact documented regression
  canaries allow-listed rather than broad test-directory exclusions;
- `actionlint` and zizmor for workflow syntax, permissions, triggers, action
  pinning, and other GitHub Actions trust boundaries;
- deterministic release reproduction, CycloneDX SBOM generation, and a
  fail-closed runtime dependency license allow-list.

GitHub Secret Scanning, push protection, and Dependabot security updates are
also enabled for the public repository. A green run means these exact checks
passed for that exact commit. It does not mean that every possible defect was
excluded.

## Real-process black-box suite

`scripts/dast-smoke` does not import a Relay handler and call it in process. It
starts a freshly built production CLI as a separate process with:

- newly generated disposable credentials;
- an isolated temporary database and storage tree;
- ephemeral loopback-only TCP listeners;
- automatic update activity disabled; and
- no model, provider, Internet, VPS, or operator configuration access.

It then treats the Relay as an external HTTP service. The current suite checks
public health behavior, security headers, missing and invalid credentials,
role boundaries, metrics access, credential-mint authority, malformed and
duplicate JSON, invalid UTF-8, body and header bounds, method restrictions,
hostile origins, producer priority-ceiling enforcement, producer ownership,
cross-producer existence-oracle resistance, cancellation authority, pairing
rate limits, response secret
leaks, and process health after hostile input.

Run the same gate locally on Linux or macOS:

```sh
mkdir -p .tmp
go build -trimpath -o .tmp/contextbridge ./cmd/contextbridge
go run ./scripts/dast-smoke --binary .tmp/contextbridge
```

On Windows PowerShell:

```powershell
New-Item -ItemType Directory -Force .tmp | Out-Null
go build -trimpath -o .tmp/contextbridge.exe ./cmd/contextbridge
go run ./scripts/dast-smoke --binary .tmp/contextbridge.exe
```

On a Unix-like development environment, `make security` runs the locally
available static, dependency, workflow, history-secret, and black-box gates in
one command. CodeQL and zizmor remain GitHub-hosted gates because their managed
analysis and SARIF lifecycle are part of the evidence being tested.

## Deliberate limits

The disposable smoke suite is not an instruction to attack a production Relay.
It currently does not claim external perimeter coverage, TLS/reverse-proxy
configuration assurance, denial-of-service capacity, multi-host network fault
injection, long-running soak evidence, cloud/IaC validation, or regulatory
compliance. Those require a named deployment and separately authorized test
scope. Field and resilience evidence remains tracked independently from CI.
