param()

$ErrorActionPreference = "Stop"
$repoRoot = [IO.Path]::GetFullPath((Join-Path $PSScriptRoot ".."))
$releaseScript = Join-Path $repoRoot "scripts\build-release.ps1"
$manifest = Get-Content -LiteralPath (Join-Path $repoRoot "extension\manifests\chromium.json") -Raw | ConvertFrom-Json
$currentVersion = "v$($manifest.version)"
$shell = (Get-Process -Id $PID).Path
$testRoot = Join-Path ([IO.Path]::GetTempPath()) ("contextbridge-release-test-" + [guid]::NewGuid().ToString("N"))

function Invoke-Check([string]$Script, [string]$Version, [string]$OutputPath) {
    $previousPreference = $ErrorActionPreference
    try {
        # Windows PowerShell 5.1 wraps a child process' stderr as error records.
        # Keep expected negative cases observable instead of turning them into a
        # terminating error in this test harness.
        $ErrorActionPreference = "Continue"
        $output = & $shell -NoProfile -File $Script -Version $Version -OutputDirectory $OutputPath -VerifyOnly *>&1
        $exitCode = $LASTEXITCODE
    } finally {
        $ErrorActionPreference = $previousPreference
    }
    return @{ ExitCode = $exitCode; Output = ($output -join "`n") }
}

try {
    New-Item -ItemType Directory -Path $testRoot | Out-Null

    $successOutput = Join-Path $testRoot "success-output"
    $success = Invoke-Check $releaseScript $currentVersion $successOutput
    if ($success.ExitCode -ne 0) {
        throw "Release preflight rejected matching extension packages: $($success.Output)"
    }
    if (Test-Path -LiteralPath $successOutput) {
        throw "VerifyOnly unexpectedly created a release output directory."
    }

    $unstable = Invoke-Check $releaseScript "$currentVersion-rc.1" (Join-Path $testRoot "unstable-output")
    if ($unstable.ExitCode -eq 0) {
        throw "Release preflight accepted a prerelease version."
    }

    $mismatchOutput = Join-Path $testRoot "mismatch-output"
    $mismatch = Invoke-Check $releaseScript "v999.999.999" $mismatchOutput
    if ($mismatch.ExitCode -eq 0 -or (Test-Path -LiteralPath $mismatchOutput)) {
        throw "Release preflight did not fail closed on a manifest version mismatch."
    }

    $fixtureRoot = Join-Path $testRoot "fixture"
    New-Item -ItemType Directory -Path (Join-Path $fixtureRoot "scripts") -Force | Out-Null
    Copy-Item -LiteralPath $releaseScript -Destination (Join-Path $fixtureRoot "scripts\build-release.ps1")
    Copy-Item -LiteralPath (Join-Path $repoRoot "extension") -Destination (Join-Path $fixtureRoot "extension") -Recurse
    Add-Content -LiteralPath (Join-Path $fixtureRoot "extension\chromium\popup.js") -Value "// stale package fixture"
    $staleOutput = Join-Path $testRoot "stale-output"
    $stale = Invoke-Check (Join-Path $fixtureRoot "scripts\build-release.ps1") $currentVersion $staleOutput
    if ($stale.ExitCode -eq 0 -or (Test-Path -LiteralPath $staleOutput)) {
        throw "Release preflight did not fail closed on a stale extension package."
    }

    $failedBuildOutput = Join-Path $testRoot "failed-build-output"
    $previousPreference = $ErrorActionPreference
    try {
        $ErrorActionPreference = "Continue"
        $failedBuildLog = & $shell -NoProfile -File $releaseScript -Version $currentVersion -OutputDirectory $failedBuildOutput -GoExecutable $shell *>&1
        $failedBuildExitCode = $LASTEXITCODE
    } finally {
        $ErrorActionPreference = $previousPreference
    }
    if ($failedBuildExitCode -eq 0 -or (Test-Path -LiteralPath $failedBuildOutput)) {
        throw "A failed binary build published a partial release: $($failedBuildLog -join "`n")"
    }
    if (@(Get-ChildItem -LiteralPath $testRoot -Directory -Filter ".contextbridge-release-*").Count -ne 0) {
        throw "A failed binary build left a release staging directory behind."
    }

    Write-Host "Release build preflight verified" -ForegroundColor Green
} finally {
    if (Test-Path -LiteralPath $testRoot) {
        $resolved = [IO.Path]::GetFullPath($testRoot)
        $tempPrefix = [IO.Path]::GetFullPath([IO.Path]::GetTempPath()).TrimEnd([IO.Path]::DirectorySeparatorChar) + [IO.Path]::DirectorySeparatorChar
        if (-not $resolved.StartsWith($tempPrefix, [StringComparison]::OrdinalIgnoreCase) -or
            -not ([IO.Path]::GetFileName($resolved) -match '^contextbridge-release-test-[0-9a-f]{32}$')) {
            throw "Refusing to remove an unexpected test directory: $resolved"
        }
        Remove-Item -LiteralPath $resolved -Recurse -Force
    }
}
