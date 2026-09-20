# Contributing

1. Open an issue before changing the public protocol or trust model.
2. Keep the core vendor-neutral. Product-specific adapters belong out of tree.
3. Never commit credentials, prompts, results, private URLs, or personal data.
4. Add regression tests for every bug fix.
5. Preserve fail-closed behavior at authentication, cost, capability,
   ownership, update, and artifact boundaries.

Before opening a pull request:

```sh
gofmt -w ./cmd ./internal ./scripts
go test ./...
go vet ./...
go test -race ./internal/bridge ./internal/cluster
```

Use `apply_patch`-sized, reviewable changes. Do not weaken a hard requirement
into an inferred preference merely to make a test pass.
