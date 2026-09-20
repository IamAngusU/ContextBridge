param(
    [string]$GoExecutable = "go"
)

$ErrorActionPreference = "Stop"
$repoRoot = [IO.Path]::GetFullPath((Join-Path $PSScriptRoot ".."))
$temporaryRoot = Join-Path ([IO.Path]::GetTempPath()) ("contextbridge-repro-" + [guid]::NewGuid().ToString("N"))
$first = Join-Path $temporaryRoot "first"
$second = Join-Path $temporaryRoot "second"
$originalEpoch = $env:SOURCE_DATE_EPOCH

function Read-ReleaseHashes([string]$Directory) {
    $hashes = @{}
    foreach ($file in Get-ChildItem -LiteralPath $Directory -File) {
        $hashes[$file.Name] = (Get-FileHash -LiteralPath $file.FullName -Algorithm SHA256).Hash.ToLowerInvariant()
    }
    return $hashes
}

try {
    New-Item -ItemType Directory -Path $temporaryRoot | Out-Null
    $env:SOURCE_DATE_EPOCH = $null
    & (Join-Path $PSScriptRoot "build-release.ps1") -Version "v0.0.0" -OutputDirectory $first -GoExecutable $GoExecutable
    if (-not $?) { throw "First release build failed." }
    & (Join-Path $PSScriptRoot "build-release.ps1") -Version "v0.0.0" -OutputDirectory $second -GoExecutable $GoExecutable
    if (-not $?) { throw "Second release build failed." }

    $firstHashes = Read-ReleaseHashes $first
    $secondHashes = Read-ReleaseHashes $second
    if ($firstHashes.Count -ne 9 -or $secondHashes.Count -ne 9) {
        throw "Expected six platform archives, one source archive, BUILD-PROVENANCE.json, and SHA256SUMS in each release."
    }
    foreach ($name in $firstHashes.Keys) {
        if (-not $secondHashes.ContainsKey($name)) {
            throw "Second release is missing $name."
        }
        if ($firstHashes[$name] -ne $secondHashes[$name]) {
            throw "Release output is not reproducible: $name differs."
        }
    }

    $record = Get-Content -LiteralPath (Join-Path $first "BUILD-PROVENANCE.json") -Raw | ConvertFrom-Json
    if ($record.kind -ne "unsigned-build-record" -or $record.signature_status -ne "unsigned") {
        throw "Build record does not state its unsigned trust boundary."
    }
    if ($record.subjects.Count -ne 7 -or -not ($record.source_commit -match '^[0-9a-f]{40,64}$')) {
        throw "Build record does not identify all archives or the exact source commit."
    }
    $sourceAssetName = "contextbridge_v0.0.0_source.tar.gz"
    if ($record.corresponding_source.asset -ne $sourceAssetName -or
        $record.corresponding_source.exact_commit -ne $record.source_commit -or
        -not $record.corresponding_source.vendored_go_modules) {
        throw "Build record does not identify the complete corresponding-source asset."
    }
    $sourceArchive = Join-Path $first $sourceAssetName
    if (-not (Test-Path -LiteralPath $sourceArchive -PathType Leaf)) {
        throw "Release does not contain the corresponding-source archive."
    }
    $sourceEntries = @(& tar -tzf $sourceArchive)
    if ($LASTEXITCODE -ne 0) {
        throw "Corresponding-source archive could not be inspected."
    }
    foreach ($requiredSource in @("SOURCE.md", "LICENSE", "go.mod", "cmd/contextbridge/main.go", "scripts/build-release.ps1", "vendor/modules.txt")) {
        if ($sourceEntries -notcontains $requiredSource) {
            throw "Corresponding-source archive does not contain $requiredSource."
        }
    }

    Add-Type -AssemblyName System.IO.Compression.FileSystem
    $zipPath = Join-Path $first "contextbridge_windows_amd64.zip"
    $zip = [IO.Compression.ZipFile]::OpenRead($zipPath)
    try {
        $sbomEntry = $zip.Entries | Where-Object { $_.FullName -eq "SBOM.cdx.json" } | Select-Object -First 1
        if (-not $sbomEntry) {
            throw "Windows archive does not contain SBOM.cdx.json."
        }
        $reader = New-Object IO.StreamReader($sbomEntry.Open())
        try {
            $sbom = $reader.ReadToEnd() | ConvertFrom-Json
        } finally {
            $reader.Dispose()
        }
        if ($sbom.bomFormat -ne "CycloneDX" -or $sbom.specVersion -ne "1.5" -or $sbom.metadata.component.name -ne "ContextBridge") {
            throw "Embedded SBOM is not the expected CycloneDX document."
        }
        if (-not $sbom.metadata.component.licenses -or $sbom.metadata.component.licenses[0].license.id -ne "AGPL-3.0-only") {
            throw "Embedded SBOM does not identify the ContextBridge core license."
        }
        $noticeEntry = $zip.Entries | Where-Object { $_.FullName -eq "THIRD_PARTY_NOTICES.txt" } | Select-Object -First 1
        if (-not $noticeEntry) {
            throw "Windows archive does not contain THIRD_PARTY_NOTICES.txt."
        }
        foreach ($requiredEntry in @("LICENSE", "LICENSING.md", "LICENSES/Apache-2.0.txt", "NOTICE", "SOURCE.md", "TRADEMARKS.md")) {
            if (-not ($zip.Entries | Where-Object { $_.FullName -eq $requiredEntry } | Select-Object -First 1)) {
                throw "Windows archive does not contain $requiredEntry."
            }
        }
        $sourceEntry = $zip.Entries | Where-Object { $_.FullName -eq "SOURCE.md" } | Select-Object -First 1
        $sourceReader = New-Object IO.StreamReader($sourceEntry.Open())
        try {
            $sourceOffer = $sourceReader.ReadToEnd()
        } finally {
            $sourceReader.Dispose()
        }
        if (-not $sourceOffer.Contains($record.source_commit) -or -not $sourceOffer.Contains($sourceAssetName)) {
            throw "Binary archive source offer is not bound to the release commit and source asset."
        }
        $noticeReader = New-Object IO.StreamReader($noticeEntry.Open())
        try {
            $notices = $noticeReader.ReadToEnd()
        } finally {
            $noticeReader.Dispose()
        }
        foreach ($component in $sbom.components) {
            if (-not $component.licenses -or -not $notices.Contains("Component: $($component.name)@$($component.version)")) {
                throw "Runtime component $($component.name)@$($component.version) lacks SBOM license metadata or bundled notice text."
            }
        }
    } finally {
        $zip.Dispose()
    }

    Write-Host "Release outputs are byte-identical and contain the expected SBOM and build record." -ForegroundColor Green
} finally {
    $env:SOURCE_DATE_EPOCH = $originalEpoch
    if (Test-Path -LiteralPath $temporaryRoot) {
        $resolved = [IO.Path]::GetFullPath($temporaryRoot)
        $tempPrefix = [IO.Path]::GetFullPath([IO.Path]::GetTempPath()).TrimEnd([IO.Path]::DirectorySeparatorChar) + [IO.Path]::DirectorySeparatorChar
        if (-not $resolved.StartsWith($tempPrefix, [StringComparison]::OrdinalIgnoreCase) -or
            -not ([IO.Path]::GetFileName($resolved) -match '^contextbridge-repro-[0-9a-f]{32}$')) {
            throw "Refusing to remove an unexpected reproducibility directory: $resolved"
        }
        Remove-Item -LiteralPath $temporaryRoot -Recurse -Force
    }
}
