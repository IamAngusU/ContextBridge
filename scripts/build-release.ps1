param(
    [Parameter(Mandatory = $true)][ValidatePattern('^v\d+\.\d+\.\d+$')][string]$Version,
    [string]$OutputDirectory = "",
    [string]$GoExecutable = "go",
    [switch]$VerifyOnly
)

$ErrorActionPreference = "Stop"
$repoRoot = [IO.Path]::GetFullPath((Join-Path $PSScriptRoot ".."))
. (Join-Path $PSScriptRoot "release-helpers.ps1")

if (-not ($Version -match '^v\d+\.\d+\.\d+$')) {
    throw "Stable releases require a version in the form vN.N.N."
}
$parsedVersion = [Version]$Version.Substring(1)
$minimumAGPLRelease = [Version]"0.7.0"
if ($parsedVersion -lt $minimumAGPLRelease) {
    throw "This AGPL release builder refuses versions below v0.7.0. Published v0.6.0 through v0.6.3 remain MIT releases."
}
if ($VerifyOnly) {
    Write-Host "Release version $Version is valid." -ForegroundColor Green
    return
}
$gitCommand = Get-Command git -ErrorAction Stop
$sourceCommit = (& $gitCommand.Source -C $repoRoot rev-parse HEAD).Trim()
if ($LASTEXITCODE -ne 0 -or -not ($sourceCommit -match '^[0-9a-f]{40,64}$')) {
    throw "Release source commit could not be resolved."
}
$workingTreeStatus = @(& $gitCommand.Source -C $repoRoot status --porcelain --untracked-files=all)
if ($LASTEXITCODE -ne 0) {
    throw "Release source cleanliness could not be verified."
}
if ($workingTreeStatus.Count -ne 0) {
    throw "Release builds require a clean Git worktree. Commit or remove local changes first."
}
$sourceDateEpoch = $env:SOURCE_DATE_EPOCH
if (-not $sourceDateEpoch) {
    $sourceDateEpoch = (& $gitCommand.Source -C $repoRoot show -s --format=%ct HEAD).Trim()
}
$epochValue = 0L
if (-not [Int64]::TryParse($sourceDateEpoch, [ref]$epochValue) -or $epochValue -lt 0) {
    throw "SOURCE_DATE_EPOCH must be a non-negative Unix timestamp."
}
if (-not $OutputDirectory) {
    $OutputDirectory = Join-Path $repoRoot "release-artifacts\$Version"
}
$outputPath = [IO.Path]::GetFullPath($OutputDirectory)
if (Test-Path -LiteralPath $outputPath) {
    throw "Release output already exists: $outputPath"
}
$outputParent = Split-Path -Parent $outputPath
New-Item -ItemType Directory -Path $outputParent -Force | Out-Null
$releaseStage = Join-Path $outputParent (".contextbridge-release-" + [guid]::NewGuid().ToString("N"))

$goCommand = Get-Command $GoExecutable -ErrorAction Stop
$temporary = Join-Path ([IO.Path]::GetTempPath()) ("contextbridge-release-" + [guid]::NewGuid().ToString("N"))
$originalGOOS = $env:GOOS
$originalGOARCH = $env:GOARCH
$originalCGO = $env:CGO_ENABLED
$originalSourceDateEpoch = $env:SOURCE_DATE_EPOCH

function Copy-BundleFiles([string]$Destination) {
    foreach ($directory in @("examples", "deploy", "docs", "LICENSES")) {
        Copy-Item -LiteralPath (Join-Path $repoRoot $directory) -Destination (Join-Path $Destination $directory) -Recurse
    }
    foreach ($file in @("config.example.yml", "README.md", "LICENSE", "LICENSING.md", "NOTICE", "TRADEMARKS.md", "THIRD_PARTY_NOTICES.txt", "CHANGELOG.md", "install.sh", "install.ps1")) {
        Copy-Item -LiteralPath (Join-Path $repoRoot $file) -Destination (Join-Path $Destination $file)
    }
}

function Write-SourceOffer([string]$Destination, [string]$SourceAssetName) {
    $text = @"
# ContextBridge $Version Corresponding Source

Source repository: https://github.com/IamAngusU/ContextBridge
Exact source commit: $sourceCommit
Release tag: $Version
Corresponding-source asset: $SourceAssetName

The complete corresponding source offered with this binary release, including
the build and installation scripts and vendored Go module source used by the
release build, is published as the source asset above on the same GitHub
release:

https://github.com/IamAngusU/ContextBridge/releases/tag/$Version

The source asset and this binary were generated from the exact commit stated
above. Verify their digests against SHA256SUMS and BUILD-PROVENANCE.json.
"@
    $text = $text.Replace([Environment]::NewLine, [string][char]10)
    $encoding = New-Object Text.UTF8Encoding($false)
    [IO.File]::WriteAllText((Join-Path $Destination "SOURCE.md"), ($text.TrimEnd() + [char]10), $encoding)
}

function Copy-TrackedSource([string]$Destination) {
    New-Item -ItemType Directory -Path $Destination -Force | Out-Null
    $tracked = @(& $gitCommand.Source -C $repoRoot ls-files)
    if ($LASTEXITCODE -ne 0 -or $tracked.Count -eq 0) {
        throw "Tracked source files could not be enumerated."
    }
    foreach ($relative in $tracked) {
        if ([IO.Path]::IsPathRooted($relative) -or $relative -match '(^|[\\/])\.\.([\\/]|$)') {
            throw "Git returned an unsafe tracked path: $relative"
        }
        $source = [IO.Path]::GetFullPath((Join-Path $repoRoot $relative))
        $rootPrefix = $repoRoot.TrimEnd([IO.Path]::DirectorySeparatorChar) + [IO.Path]::DirectorySeparatorChar
        if (-not $source.StartsWith($rootPrefix, [StringComparison]::OrdinalIgnoreCase)) {
            throw "Tracked source escaped the repository: $relative"
        }
        $item = Get-Item -LiteralPath $source -Force
        if ($item.PSIsContainer -or ($item.Attributes -band [IO.FileAttributes]::ReparsePoint)) {
            throw "Tracked source is not a regular file: $relative"
        }
        $target = Join-Path $Destination $relative
        $targetParent = Split-Path -Parent $target
        if ($targetParent) {
            New-Item -ItemType Directory -Path $targetParent -Force | Out-Null
        }
        Copy-Item -LiteralPath $source -Destination $target
    }
}

try {
    New-Item -ItemType Directory -Path $releaseStage | Out-Null
    New-Item -ItemType Directory -Path $temporary -Force | Out-Null
    $archiveTool = Join-Path $temporary "contextbridge-release-archive.exe"
    $sbomTool = Join-Path $temporary "contextbridge-release-sbom.exe"
    $env:SOURCE_DATE_EPOCH = $sourceDateEpoch
    $env:GOOS = $originalGOOS
    $env:GOARCH = $originalGOARCH
    $env:CGO_ENABLED = $originalCGO
    & $goCommand.Source build -trimpath -o $archiveTool ./scripts/release-archive
    if ($LASTEXITCODE -ne 0) { throw "Release archive helper could not be built." }
    & $goCommand.Source build -trimpath -o $sbomTool ./scripts/release-sbom
    if ($LASTEXITCODE -ne 0) { throw "Release SBOM helper could not be built." }

    $sourceAssetName = "contextbridge_$($Version)_source.tar.gz"
    $sourceStage = Join-Path $temporary "corresponding-source"
    Copy-TrackedSource $sourceStage
    & $goCommand.Source mod vendor -o (Join-Path $sourceStage "vendor")
    if ($LASTEXITCODE -ne 0) { throw "Vendoring corresponding source failed." }
    Write-SourceOffer $sourceStage $sourceAssetName
    & $archiveTool $sourceStage (Join-Path $releaseStage $sourceAssetName)
    if ($LASTEXITCODE -ne 0) { throw "Corresponding-source archive creation failed." }

    $platforms = @(
        @{ OS = "windows"; Arch = "amd64"; Extension = ".exe"; Format = "zip" },
        @{ OS = "windows"; Arch = "arm64"; Extension = ".exe"; Format = "zip" },
        @{ OS = "linux"; Arch = "amd64"; Extension = ""; Format = "tar" },
        @{ OS = "linux"; Arch = "arm64"; Extension = ""; Format = "tar" },
        @{ OS = "darwin"; Arch = "amd64"; Extension = ""; Format = "tar" },
        @{ OS = "darwin"; Arch = "arm64"; Extension = ""; Format = "tar" }
    )

    foreach ($platform in $platforms) {
        $env:GOOS = $platform.OS
        $env:GOARCH = $platform.Arch
        $env:CGO_ENABLED = "0"
        $stage = Join-Path $temporary ("$($platform.OS)-$($platform.Arch)")
        New-Item -ItemType Directory -Path $stage -Force | Out-Null
        $binary = Join-Path $stage ("contextbridge" + $platform.Extension)
        & $goCommand.Source build -trimpath -buildvcs=true -ldflags "-s -w -X main.version=$Version" -o $binary ./cmd/contextbridge
        if ($LASTEXITCODE -ne 0) { throw "Go build failed for $($platform.OS)/$($platform.Arch)." }
        Copy-BundleFiles $stage
        Write-SourceOffer $stage $sourceAssetName
        & $sbomTool $binary $Version $sourceDateEpoch (Join-Path $stage "SBOM.cdx.json")
        if ($LASTEXITCODE -ne 0) { throw "SBOM generation failed for $($platform.OS)/$($platform.Arch)." }

        $baseName = "contextbridge_$($platform.OS)_$($platform.Arch)"
        if ($platform.Format -eq "zip") {
            $asset = Join-Path $releaseStage "$baseName.zip"
        } else {
            $asset = Join-Path $releaseStage "$baseName.tar.gz"
        }
        & $archiveTool $stage $asset
        if ($LASTEXITCODE -ne 0) { throw "Archive creation failed for $baseName." }
    }

    $subjects = @(Get-ChildItem -LiteralPath $releaseStage -File | Sort-Object Name | ForEach-Object {
        [ordered]@{
            name = $_.Name
            sha256 = (Get-FileHash -LiteralPath $_.FullName -Algorithm SHA256).Hash.ToLowerInvariant()
            size_bytes = $_.Length
        }
    })
    $provenance = [ordered]@{
        schema_version = 1
        kind = "unsigned-build-record"
        release = $Version
        source_repository = "https://github.com/IamAngusU/ContextBridge"
        source_commit = $sourceCommit
        source_date_epoch = $epochValue
        builder = [ordered]@{
            tool = "scripts/build-release.ps1"
            go = ((& $goCommand.Source version) -join " ").Trim()
        }
        reproducibility = [ordered]@{
            clean_git_worktree_required = $true
            trimpath = $true
            cgo_enabled = $false
            normalized_archive_metadata = $true
        }
        signature_status = "unsigned"
        corresponding_source = [ordered]@{
            asset = $sourceAssetName
            exact_commit = $sourceCommit
            vendored_go_modules = $true
        }
        subjects = $subjects
    }
    $utf8NoBom = New-Object Text.UTF8Encoding($false)
    $provenanceJSON = ($provenance | ConvertTo-Json -Depth 8) -replace "`r`n", "`n"
    [IO.File]::WriteAllText((Join-Path $releaseStage "BUILD-PROVENANCE.json"), ($provenanceJSON + "`n"), $utf8NoBom)
    Write-ContextBridgeChecksumFile -Directory $releaseStage -Path (Join-Path $releaseStage "SHA256SUMS")
    if (Test-Path -LiteralPath $outputPath) {
        throw "Release output appeared while the build was running: $outputPath"
    }
    [IO.Directory]::Move($releaseStage, $outputPath)
    Write-Host "Release $Version built in $outputPath" -ForegroundColor Green
} finally {
    $env:GOOS = $originalGOOS
    $env:GOARCH = $originalGOARCH
    $env:CGO_ENABLED = $originalCGO
    $env:SOURCE_DATE_EPOCH = $originalSourceDateEpoch
    if (Test-Path -LiteralPath $releaseStage) {
        $resolvedStage = [IO.Path]::GetFullPath($releaseStage)
        $parentPrefix = [IO.Path]::GetFullPath($outputParent).TrimEnd([IO.Path]::DirectorySeparatorChar) + [IO.Path]::DirectorySeparatorChar
        if (-not $resolvedStage.StartsWith($parentPrefix, [StringComparison]::OrdinalIgnoreCase) -or
            -not ([IO.Path]::GetFileName($resolvedStage) -match '^\.contextbridge-release-[0-9a-f]{32}$')) {
            throw "Refusing to remove an unexpected release staging directory: $resolvedStage"
        }
        Remove-Item -LiteralPath $releaseStage -Recurse -Force
    }
    if (Test-Path -LiteralPath $temporary) {
        $resolvedTemp = [IO.Path]::GetFullPath($temporary)
        $tempRoot = [IO.Path]::GetFullPath([IO.Path]::GetTempPath()).TrimEnd([IO.Path]::DirectorySeparatorChar) + [IO.Path]::DirectorySeparatorChar
        if (-not $resolvedTemp.StartsWith($tempRoot, [StringComparison]::OrdinalIgnoreCase) -or
            -not ([IO.Path]::GetFileName($resolvedTemp) -match '^contextbridge-release-[0-9a-f]{32}$')) {
            throw "Refusing to remove an unexpected temporary directory: $resolvedTemp"
        }
        Remove-Item -LiteralPath $temporary -Recurse -Force
    }
}
