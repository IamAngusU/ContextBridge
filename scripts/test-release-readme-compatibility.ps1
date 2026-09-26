$ErrorActionPreference = "Stop"
Set-StrictMode -Version Latest

$repoRoot = Split-Path -Parent $PSScriptRoot
$manifestPath = Join-Path $repoRoot "docs/release-compatibility.json"
$readmePath = Join-Path $repoRoot "README.md"
$manifest = Get-Content -LiteralPath $manifestPath -Raw | ConvertFrom-Json

if ($manifest.schema -ne "contextbridge.release-readme-compatibility.v1" -or
    $manifest.latest_release -notmatch '^v\d+\.\d+\.\d+$') {
    throw "Invalid release compatibility manifest."
}

$release = Invoke-RestMethod -Uri "https://api.github.com/repos/IamAngusU/ContextBridge/releases/latest" -Headers @{ Accept = "application/vnd.github+json" }
if ($release.tag_name -ne $manifest.latest_release) {
    throw "docs/release-compatibility.json tracks $($manifest.latest_release), but GitHub latest is $($release.tag_name). Update the release contract and remove labels for commands that now ship."
}

$architecture = [System.Runtime.InteropServices.RuntimeInformation]::ProcessArchitecture.ToString().ToLowerInvariant()
if ($architecture -ne "x64" -and $architecture -ne "arm64") {
    throw "Unsupported test architecture $architecture."
}
$assetArchitecture = if ($architecture -eq "x64") { "amd64" } else { "arm64" }
$platform = if ($IsWindows) { "windows" } elseif ($IsLinux) { "linux" } elseif ($IsMacOS) { "darwin" } else { throw "Unsupported test platform." }
$extension = if ($IsWindows) { "zip" } else { "tar.gz" }
$asset = "contextbridge_${platform}_${assetArchitecture}.${extension}"
$base = "https://github.com/IamAngusU/ContextBridge/releases/download/$($manifest.latest_release)"

$temporary = Join-Path ([System.IO.Path]::GetTempPath()) ("contextbridge-release-contract-" + [guid]::NewGuid().ToString("N"))
New-Item -ItemType Directory -Path $temporary | Out-Null
try {
    $archive = Join-Path $temporary $asset
    $checksums = Join-Path $temporary "SHA256SUMS"
    Invoke-WebRequest -Uri "$base/$asset" -OutFile $archive
    Invoke-WebRequest -Uri "$base/SHA256SUMS" -OutFile $checksums

    $checksumLine = Get-Content -LiteralPath $checksums | Where-Object { $_ -match "^[0-9a-fA-F]{64}\s+\*?$([regex]::Escape($asset))$" } | Select-Object -First 1
    if (-not $checksumLine) {
        throw "SHA256SUMS does not contain $asset."
    }
    $expectedHash = ($checksumLine -split '\s+')[0].ToLowerInvariant()
    $actualHash = (Get-FileHash -LiteralPath $archive -Algorithm SHA256).Hash.ToLowerInvariant()
    if ($actualHash -ne $expectedHash) {
        throw "Checksum mismatch for $asset."
    }

    $extract = Join-Path $temporary "release"
    New-Item -ItemType Directory -Path $extract | Out-Null
    if ($IsWindows) {
        Expand-Archive -LiteralPath $archive -DestinationPath $extract
    } else {
        & tar -xzf $archive -C $extract
        if ($LASTEXITCODE -ne 0) { throw "Could not extract $asset." }
    }
    $binaryName = if ($IsWindows) { "contextbridge.exe" } else { "contextbridge" }
    $binary = Get-ChildItem -LiteralPath $extract -Recurse -File -Filter $binaryName | Select-Object -First 1 -ExpandProperty FullName
    if (-not $binary) {
        throw "Release archive did not contain $binaryName."
    }

    function Invoke-ReleaseCommand([object[]]$Arguments) {
        $previousPreference = $ErrorActionPreference
        $ErrorActionPreference = "Continue"
        try {
            $output = & $binary @Arguments 2>&1 | Out-String
            return $output
        } finally {
            $ErrorActionPreference = $previousPreference
        }
    }

    foreach ($check in $manifest.release_commands) {
        $output = Invoke-ReleaseCommand @($check.args)
        if ($output.Contains([string]$check.output_must_not_contain, [System.StringComparison]::Ordinal)) {
            throw "Published $($manifest.latest_release) rejected documented release command '$($check.args -join ' ')': $output"
        }
    }

    $readme = Get-Content -LiteralPath $readmePath -Raw
    foreach ($check in $manifest.main_only_commands) {
        if (-not $readme.Contains([string]$check.readme_marker, [System.StringComparison]::Ordinal)) {
            throw "README is missing release-coherence marker '$($check.readme_marker)'."
        }
        $output = Invoke-ReleaseCommand @($check.args)
        if ($check.PSObject.Properties.Name -contains "release_output_must_contain" -and
            -not $output.Contains([string]$check.release_output_must_contain, [System.StringComparison]::Ordinal)) {
            throw "Published $($manifest.latest_release) may now support '$($check.args -join ' ')'. Update README and manifest. Output: $output"
        }
        if ($check.PSObject.Properties.Name -contains "release_output_must_not_contain" -and
            $output.Contains([string]$check.release_output_must_not_contain, [System.StringComparison]::Ordinal)) {
            throw "Published $($manifest.latest_release) may now support '$($check.args -join ' ')'. Update README and manifest. Output: $output"
        }
    }

    if (-not $readme.Contains('This README tracks current `main`', [System.StringComparison]::Ordinal)) {
        throw "README lost its release/main applicability warning."
    }
    Write-Output "README release contract matches published $($manifest.latest_release) on $platform-$assetArchitecture."
} finally {
    Remove-Item -LiteralPath $temporary -Recurse -Force -ErrorAction SilentlyContinue
}
