param(
    [Parameter(Mandatory = $true)][ValidatePattern('^v\d+\.\d+\.\d+$')][string]$Version,
    [string]$OutputDirectory = "",
    [string]$GoExecutable = "go",
    [switch]$VerifyOnly
)

$ErrorActionPreference = "Stop"
$repoRoot = [IO.Path]::GetFullPath((Join-Path $PSScriptRoot ".."))
$extensionRoot = Join-Path $repoRoot "extension"
$expectedExtensionVersion = $Version.Substring(1)

function Get-RelativeFileNames([string]$Root) {
    $prefix = [IO.Path]::GetFullPath($Root).TrimEnd([IO.Path]::DirectorySeparatorChar, [IO.Path]::AltDirectorySeparatorChar) + [IO.Path]::DirectorySeparatorChar
    return @(Get-ChildItem -LiteralPath $Root -File -Recurse | ForEach-Object {
        $_.FullName.Substring($prefix.Length).Replace([IO.Path]::DirectorySeparatorChar, '/')
    } | Sort-Object)
}

function Assert-SameFile([string]$Expected, [string]$Actual) {
    if (-not (Test-Path -LiteralPath $Expected -PathType Leaf) -or -not (Test-Path -LiteralPath $Actual -PathType Leaf)) {
        throw "Extension package file is missing: $Actual"
    }
    if ((Get-Item -LiteralPath $Expected).Length -le 0 -or (Get-Item -LiteralPath $Actual).Length -le 0) {
        throw "Extension package file is empty: $Actual"
    }
    $expectedHash = (Get-FileHash -LiteralPath $Expected -Algorithm SHA256).Hash
    $actualHash = (Get-FileHash -LiteralPath $Actual -Algorithm SHA256).Hash
    if ($expectedHash -ne $actualHash) {
        throw "Extension package is stale: $Actual does not match $Expected"
    }
}

function Read-ManifestVersion([string]$Path) {
    if (-not (Test-Path -LiteralPath $Path -PathType Leaf)) {
        throw "Extension manifest is missing: $Path"
    }
    try {
        $manifest = Get-Content -LiteralPath $Path -Raw | ConvertFrom-Json
    } catch {
        throw "Extension manifest is not valid JSON: $Path"
    }
    $manifestVersion = [string]$manifest.version
    if (-not ($manifestVersion -match '^\d+\.\d+\.\d+$')) {
        throw "Extension manifest has an invalid stable version: $Path"
    }
    return $manifestVersion
}

function Assert-ExtensionReleaseState([string]$ExpectedVersion) {
    $sourceRoot = Join-Path $extensionRoot "src"
    $sourceFiles = Get-RelativeFileNames $sourceRoot
    if ($sourceFiles.Count -eq 0) {
        throw "Extension source is empty: $sourceRoot"
    }
    foreach ($browser in @("chromium", "firefox")) {
        $sourceManifest = Join-Path $extensionRoot "manifests\$browser.json"
        $packageRoot = Join-Path $extensionRoot $browser
        $packageManifest = Join-Path $packageRoot "manifest.json"
        foreach ($manifestPath in @($sourceManifest, $packageManifest)) {
            $actualVersion = Read-ManifestVersion $manifestPath
            if ($actualVersion -ne $ExpectedVersion) {
                throw "Extension manifest version $actualVersion does not match requested release $ExpectedVersion`: $manifestPath"
            }
        }
        Assert-SameFile $sourceManifest $packageManifest
        foreach ($relativeName in $sourceFiles) {
            $nativeRelativeName = $relativeName.Replace('/', [IO.Path]::DirectorySeparatorChar)
            Assert-SameFile (Join-Path $sourceRoot $nativeRelativeName) (Join-Path $packageRoot $nativeRelativeName)
        }
        $expectedPackageFiles = @($sourceFiles + "manifest.json" | Sort-Object)
        $actualPackageFiles = Get-RelativeFileNames $packageRoot
        $inventoryDifference = @(Compare-Object -ReferenceObject $expectedPackageFiles -DifferenceObject $actualPackageFiles)
        if ($inventoryDifference.Count -ne 0) {
            $details = ($inventoryDifference | ForEach-Object { "$($_.SideIndicator) $($_.InputObject)" }) -join ", "
            throw "Extension package inventory differs from source for $browser`: $details"
        }
    }
}

if (-not ($Version -match '^v\d+\.\d+\.\d+$')) {
    throw "Stable releases require a version in the form vN.N.N."
}
Assert-ExtensionReleaseState $expectedExtensionVersion
if ($VerifyOnly) {
    Write-Host "Release metadata and extension packages match $Version." -ForegroundColor Green
    return
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

function Copy-BundleFiles([string]$Destination) {
    New-Item -ItemType Directory -Path (Join-Path $Destination "extension") -Force | Out-Null
    Copy-Item -LiteralPath (Join-Path $repoRoot "extension\README.md") -Destination (Join-Path $Destination "extension\README.md")
    Copy-Item -LiteralPath (Join-Path $repoRoot "extension\chromium") -Destination (Join-Path $Destination "extension\chromium") -Recurse
    Copy-Item -LiteralPath (Join-Path $repoRoot "extension\firefox") -Destination (Join-Path $Destination "extension\firefox") -Recurse
    foreach ($directory in @("examples", "deploy", "web", "docs")) {
        Copy-Item -LiteralPath (Join-Path $repoRoot $directory) -Destination (Join-Path $Destination $directory) -Recurse
    }
    foreach ($file in @("config.example.yml", "README.md", "LICENSE", "CHANGELOG.md", "install.sh", "install.ps1")) {
        Copy-Item -LiteralPath (Join-Path $repoRoot $file) -Destination (Join-Path $Destination $file)
    }
}

try {
    New-Item -ItemType Directory -Path $releaseStage | Out-Null
    New-Item -ItemType Directory -Path $temporary -Force | Out-Null
    $archiveTool = Join-Path $temporary "contextbridge-release-archive.exe"
    $env:GOOS = $originalGOOS
    $env:GOARCH = $originalGOARCH
    $env:CGO_ENABLED = $originalCGO
    & $goCommand.Source build -trimpath -o $archiveTool ./scripts/release-archive
    if ($LASTEXITCODE -ne 0) { throw "Release archive helper could not be built." }

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
        & $goCommand.Source build -trimpath -ldflags "-s -w -X main.version=$Version" -o $binary ./cmd/contextbridge
        if ($LASTEXITCODE -ne 0) { throw "Go build failed for $($platform.OS)/$($platform.Arch)." }
        Copy-BundleFiles $stage

        $baseName = "contextbridge_$($platform.OS)_$($platform.Arch)"
        if ($platform.Format -eq "zip") {
            $asset = Join-Path $releaseStage "$baseName.zip"
            Compress-Archive -Path (Join-Path $stage "*") -DestinationPath $asset -CompressionLevel Optimal
        } else {
            $asset = Join-Path $releaseStage "$baseName.tar.gz"
            & $archiveTool $stage $asset
            if ($LASTEXITCODE -ne 0) { throw "Archive creation failed for $baseName." }
        }
    }

    foreach ($browser in @("chromium", "firefox")) {
        $source = Join-Path $repoRoot "extension\$browser"
        $asset = Join-Path $releaseStage "contextbridge_extension_$browser.zip"
        Compress-Archive -Path (Join-Path $source "*") -DestinationPath $asset -CompressionLevel Optimal
    }

    $checksumLines = Get-ChildItem -LiteralPath $releaseStage -File |
        Sort-Object Name |
        ForEach-Object { "{0}  {1}" -f (Get-FileHash -LiteralPath $_.FullName -Algorithm SHA256).Hash.ToLowerInvariant(), $_.Name }
    [IO.File]::WriteAllLines((Join-Path $releaseStage "SHA256SUMS"), $checksumLines, (New-Object Text.UTF8Encoding($false)))
    if (Test-Path -LiteralPath $outputPath) {
        throw "Release output appeared while the build was running: $outputPath"
    }
    [IO.Directory]::Move($releaseStage, $outputPath)
    Write-Host "Release $Version built in $outputPath" -ForegroundColor Green
} finally {
    $env:GOOS = $originalGOOS
    $env:GOARCH = $originalGOARCH
    $env:CGO_ENABLED = $originalCGO
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
