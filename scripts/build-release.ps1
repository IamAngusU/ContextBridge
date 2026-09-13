param(
    [Parameter(Mandatory = $true)][ValidatePattern('^v\d+\.\d+\.\d+(?:[-+][0-9A-Za-z.-]+)?$')][string]$Version,
    [string]$OutputDirectory = "",
    [string]$GoExecutable = "go"
)

$ErrorActionPreference = "Stop"
$repoRoot = [IO.Path]::GetFullPath((Join-Path $PSScriptRoot ".."))
if (-not $OutputDirectory) {
    $OutputDirectory = Join-Path $repoRoot "release-artifacts\$Version"
}
$outputPath = [IO.Path]::GetFullPath($OutputDirectory)
if (Test-Path -LiteralPath $outputPath) {
    throw "Release output already exists: $outputPath"
}

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
    foreach ($directory in @("examples", "deploy", "web")) {
        Copy-Item -LiteralPath (Join-Path $repoRoot $directory) -Destination (Join-Path $Destination $directory) -Recurse
    }
    foreach ($file in @("config.example.yml", "README.md", "LICENSE", "CHANGELOG.md", "install.sh", "install.ps1")) {
        Copy-Item -LiteralPath (Join-Path $repoRoot $file) -Destination (Join-Path $Destination $file)
    }
}

try {
    New-Item -ItemType Directory -Path $outputPath -Force | Out-Null
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
            $asset = Join-Path $outputPath "$baseName.zip"
            Compress-Archive -Path (Join-Path $stage "*") -DestinationPath $asset -CompressionLevel Optimal
        } else {
            $asset = Join-Path $outputPath "$baseName.tar.gz"
            & $archiveTool $stage $asset
            if ($LASTEXITCODE -ne 0) { throw "Archive creation failed for $baseName." }
        }
    }

    foreach ($browser in @("chromium", "firefox")) {
        $source = Join-Path $repoRoot "extension\$browser"
        $asset = Join-Path $outputPath "contextbridge_extension_$browser.zip"
        Compress-Archive -Path (Join-Path $source "*") -DestinationPath $asset -CompressionLevel Optimal
    }

    $checksumLines = Get-ChildItem -LiteralPath $outputPath -File |
        Sort-Object Name |
        ForEach-Object { "{0}  {1}" -f (Get-FileHash -LiteralPath $_.FullName -Algorithm SHA256).Hash.ToLowerInvariant(), $_.Name }
    [IO.File]::WriteAllLines((Join-Path $outputPath "SHA256SUMS"), $checksumLines, (New-Object Text.UTF8Encoding($false)))
    Write-Host "Release $Version built in $outputPath" -ForegroundColor Green
} finally {
    $env:GOOS = $originalGOOS
    $env:GOARCH = $originalGOARCH
    $env:CGO_ENABLED = $originalCGO
    if (Test-Path -LiteralPath $temporary) {
        Remove-Item -LiteralPath $temporary -Recurse -Force
    }
}
