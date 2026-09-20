# Contributing

Issues, security reports, reproducible test cases, documentation corrections,
and design discussion are welcome.

The ContextBridge core is moving to an AGPL and commercial dual-licensing
model. Do not open a core code pull request yet: no external core code can be
accepted until a professionally reviewed contributor agreement explicitly
supports that model. A DCO or an implicit license through a pull request is not
treated as permission to relicense a contribution. This temporary boundary
avoids ambiguous ownership; it is not a judgement about the size of a change.

The maintainer may independently implement a reported fix. Contributions to a
separately licensed integration repository will follow that repository's own
policy once those repositories exist.

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
