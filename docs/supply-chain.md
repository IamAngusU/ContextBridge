# Release integrity and reproducibility

ContextBridge release artifacts are built from a clean Git commit. The release
builder rejects tracked or untracked worktree changes rather than creating an
artifact whose source cannot be named exactly.

Every platform archive contains:

- the `contextbridge` executable;
- the public documentation, examples, and deployment files; and
- `SBOM.cdx.json`, a CycloneDX 1.5 software bill of materials derived from the
  Go build information embedded in that executable.

The release directory also contains:

- `SHA256SUMS`, covering every archive and the build record; and
- `BUILD-PROVENANCE.json`, recording the source commit, source epoch, builder,
  reproducibility controls, archive sizes, and archive digests.

## What this proves

The checksum file detects bytes that do not match the published release. The
SBOM exposes the dependency graph embedded in each platform binary. Rebuilding
twice with the same source commit, Go toolchain, environment, and version should
produce byte-identical release files.

The archive writer uses lexical ordering and normalizes timestamps, owner
metadata, and permissions. Builds use `-trimpath`, disabled CGO, and the Git
commit timestamp through `SOURCE_DATE_EPOCH` unless the operator deliberately
supplies another non-negative epoch.

## What this does not prove

`BUILD-PROVENANCE.json` currently declares `"signature_status": "unsigned"`.
It is a build record, not a cryptographic publisher identity, transparency-log
entry, SLSA attestation, external audit, or proof that GitHub executed the
build. A checksum served beside a release cannot by itself authenticate the
publisher of both files.

ContextBridge will not label commits, tags, provenance, or artifacts as signed
until the corresponding public key or CI identity is registered and the
signature can be verified independently.

## Reproduce a release

Use the Go version declared by `go.mod`, check out the exact release commit,
and keep the worktree clean. On Windows PowerShell:

```powershell
git status --short
$epoch = git show -s --format=%ct HEAD
$env:SOURCE_DATE_EPOCH = $epoch
$repro = Join-Path ([IO.Path]::GetTempPath()) "contextbridge-v0.6.2-repro"
pwsh ./scripts/build-release.ps1 -Version v0.6.2 -OutputDirectory "$repro-a"
pwsh ./scripts/build-release.ps1 -Version v0.6.2 -OutputDirectory "$repro-b"
```

Because absolute paths differ, a direct content comparison is clearer:

```powershell
$a = Get-ChildItem "$repro-a" -File | ForEach-Object {
  [pscustomobject]@{ Name = $_.Name; Hash = (Get-FileHash $_ -Algorithm SHA256).Hash }
}
$b = Get-ChildItem "$repro-b" -File | ForEach-Object {
  [pscustomobject]@{ Name = $_.Name; Hash = (Get-FileHash $_ -Algorithm SHA256).Hash }
}
Compare-Object ($a | Sort-Object Name) ($b | Sort-Object Name) -Property Name,Hash
```

No output from the final command means the release files are byte-identical.

To inspect one archive, extract it and parse the SBOM as JSON. To verify the
outer release set, recalculate SHA-256 for every file named in `SHA256SUMS`
before installing it.

## Evidence still to earn

Reproducible packaging is not operational maturity. Long-duration relay soak
results, failure-injection results, backup/restore drills, migration drills,
independent security review, and real deployment history must be published as
dated evidence after they actually occur. They are deliberately not inferred
from unit tests or architecture alone.
