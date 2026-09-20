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
if ($VerifyOnly) {
    Write-Host "Release version $Version is valid." -ForegroundColor Green
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
    foreach ($directory in @("examples", "deploy", "docs")) {
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
