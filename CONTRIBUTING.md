# Contributing

Thank you for improving ContextBridge. Small, focused changes are easiest to review and release.

## Before You Start

1. Search open issues and discussions for related work.
2. Open an issue before changing the public protocol, trust model, or browser permission scope.
3. Keep provider-specific behavior behind the existing provider boundary.
4. Never add remotely hosted code to a browser extension package.

## Local Setup

Requirements:

- Go 1.22 or newer
- Node.js 20 or newer for JavaScript syntax checks
- A Chromium browser or Firefox for extension testing
- Ollama only when testing the local model provider

```bash
git clone https://github.com/IamAngusU/ContextBridge.git
cd ContextBridge
go test ./...
./scripts/package-extensions.sh
./scripts/verify-extensions.sh
```

Source extension changes belong in `extension/src`. Manifest changes belong in `extension/manifests`. Run the package script before committing so the ready browser directories remain reproducible.

## Pull Requests

- Describe the user-visible problem and the chosen behavior.
- Add or update tests for protocol, routing, authentication, and storage changes.
- Test both browser manifests when extension behavior changes.
- Keep configuration examples and docs aligned with defaults.
- Do not include secrets, tokens, private endpoints, or user content in fixtures.
- Use plain, direct commit messages.

## Checks

```bash
go test ./...
go vet ./...
go build ./cmd/contextbridge
./scripts/package-extensions.sh
./scripts/verify-extensions.sh
git diff --exit-code -- extension/chromium extension/firefox
```

## Security Reports

Do not open a public issue for a vulnerability. Follow [SECURITY.md](SECURITY.md).
