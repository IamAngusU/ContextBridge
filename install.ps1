param(
    [string]$InstallDir = "$env:LOCALAPPDATA\ContextBridge",
    [ValidateSet("ask", "ollama", "managed", "browser", "later")][string]$Provider = "ask",
    [ValidateSet("ask", "local", "relay", "worker", "all")][string]$ClusterMode = "ask",
    [ValidateSet("jina", "nuextract", "both")][string]$ManagedModel = "jina",
    [string]$RelayUrl = "",
    [string]$NodeName = "auto",
    [switch]$NoAutostart,
    [switch]$NoStart,
    [switch]$NoDashboard,
    [switch]$NoPath,
    [switch]$NoCompletion
)

$ErrorActionPreference = "Stop"
$repo = "IamAngusU/ContextBridge"

function Info($Text) { Write-Host $Text -ForegroundColor Cyan }
function Good($Text) { Write-Host $Text -ForegroundColor Green }
function Muted($Text) { Write-Host $Text -ForegroundColor DarkGray }

function Get-ContextBridgeCompletionCommands {
    param([bool]$AliasInstalled)
    if ($AliasInstalled) { return 'contextbridge and cb' }
    return 'contextbridge'
}

function Test-ContextBridgeCommandPath {
    param(
        $CommandInfo,
        [Parameter(Mandatory = $true)][string]$ExpectedPath
    )
    if (-not $CommandInfo -or -not $CommandInfo.Path) { return $false }
    try {
        return [IO.Path]::GetFullPath([string]$CommandInfo.Path).Equals(
            [IO.Path]::GetFullPath($ExpectedPath),
            [StringComparison]::OrdinalIgnoreCase
        )
    } catch {
        return $false
    }
}

function Update-ContextBridgeCompletionProfile {
    param(
        [Parameter(Mandatory = $true)][string]$ProfilePath,
        [Parameter(Mandatory = $true)][string]$CompletionPath
    )

    $beginMarker = '# >>> ContextBridge completion >>>'
    $endMarker = '# <<< ContextBridge completion <<<'
    $profileText = if (Test-Path -LiteralPath $ProfilePath) { [IO.File]::ReadAllText($ProfilePath) } else { '' }
    $beginMatches = [regex]::Matches($profileText, '(?m)^# >>> ContextBridge completion >>>\r?$')
    $endMatches = [regex]::Matches($profileText, '(?m)^# <<< ContextBridge completion <<<\r?$')

    # Ownership markers are authority to replace content only when there is
    # exactly one complete, ordered block. Ambiguous or malformed markers must
    # never cause a shell profile rewrite; the caller treats this optional
    # integration as fail-soft and leaves ContextBridge itself installed.
    if ($beginMatches.Count -ne $endMatches.Count -or $beginMatches.Count -gt 1 -or
        ($beginMatches.Count -eq 1 -and $beginMatches[0].Index -ge $endMatches[0].Index)) {
        throw 'The PowerShell profile contains malformed ContextBridge completion markers; it was left unchanged.'
    }

    $newline = if ($profileText.Contains("`r`n")) { "`r`n" } elseif ($profileText.Contains("`n")) { "`n" } else { [Environment]::NewLine }
    $escapedCompletionPath = $CompletionPath.Replace("'", "''")
    $completionBlock = $beginMarker + $newline + ". '$escapedCompletionPath'" + $newline + $endMarker

    if ($beginMatches.Count -eq 1) {
        $blockStart = $beginMatches[0].Index
        $blockEnd = $endMatches[0].Index + $endMatches[0].Length
        # The end-marker match includes a CR on CRLF input. Exclude it because
        # the replacement block already supplies its own line ending semantics.
        if ($profileText[$blockEnd - 1] -eq "`r") { $blockEnd-- }
        $profileText = $profileText.Substring(0, $blockStart) + $completionBlock + $profileText.Substring($blockEnd)
    } else {
        if ($profileText -and -not $profileText.EndsWith("`n")) { $profileText += $newline }
        $profileText += $completionBlock + $newline
    }

    $profileDirectory = Split-Path -Parent $ProfilePath
    if ($profileDirectory) { New-Item -ItemType Directory -Path $profileDirectory -Force | Out-Null }
    [IO.File]::WriteAllText($ProfilePath, $profileText, (New-Object Text.UTF8Encoding($false)))
}

Info "ContextBridge installer"
Muted "A local bridge for Ollama and explicitly paired browser tabs."

if ($Provider -eq "ask") {
    Write-Host ""
    Write-Host "Choose the first local target:"
    Write-Host "  1) Existing Ollama, with automatic local model detection (recommended)"
    Write-Host "  2) Managed llama.cpp runtime and a verified GGUF model"
    Write-Host "  3) An auto-detected or visually taught browser tab"
    Write-Host "  4) Configure it later in YAML"
    $choice = Read-Host "Choose 1, 2, 3, or 4 [1]"
    if (-not $choice) { $choice = "1" }
    $Provider = if ($choice -eq "2") { "managed" } elseif ($choice -eq "3") { "browser" } elseif ($choice -eq "4") { "later" } else { "ollama" }
    if ($Provider -eq "managed") {
        Write-Host ""
        Write-Host "Choose the first managed workload:"
        Write-Host "  1) Jina v4 retrieval embeddings"
        Write-Host "  2) NuExtract3 structured extraction"
        Write-Host "  3) Both models"
        $modelChoice = Read-Host "Choose 1, 2, or 3 [1]"
        $ManagedModel = if ($modelChoice -eq "2") { "nuextract" } elseif ($modelChoice -eq "3") { "both" } else { "jina" }
    }
}

$architecture = if ($env:PROCESSOR_ARCHITECTURE -eq "ARM64") { "arm64" } else { "amd64" }
$release = Invoke-RestMethod -Uri "https://api.github.com/repos/$repo/releases/latest" -Headers @{ "User-Agent" = "ContextBridge-Installer" }
$assetName = "contextbridge_windows_$architecture.zip"
$asset = $release.assets | Where-Object { $_.name -eq $assetName } | Select-Object -First 1
if (-not $asset) { throw "Release asset $assetName was not found." }
$checksumAsset = $release.assets | Where-Object { $_.name -eq "SHA256SUMS" } | Select-Object -First 1
if (-not $checksumAsset) { throw "Release checksums were not found." }

$temporary = Join-Path ([IO.Path]::GetTempPath()) ("contextbridge-" + [guid]::NewGuid().ToString("N"))
New-Item -ItemType Directory -Force -Path $temporary | Out-Null
try {
    Info "Downloading $($release.tag_name) for Windows $architecture..."
    $archive = Join-Path $temporary $assetName
    Invoke-WebRequest -Uri $asset.browser_download_url -OutFile $archive -UseBasicParsing
    $checksums = Join-Path $temporary "SHA256SUMS"
    Invoke-WebRequest -Uri $checksumAsset.browser_download_url -OutFile $checksums -UseBasicParsing
    $checksumLine = Get-Content $checksums | Where-Object { $_ -match "\s$([regex]::Escape($assetName))$" } | Select-Object -First 1
    if (-not $checksumLine) { throw "No checksum was published for $assetName." }
    $expectedHash = ($checksumLine -split '\s+')[0].ToLowerInvariant()
    $actualHash = (Get-FileHash -Path $archive -Algorithm SHA256).Hash.ToLowerInvariant()
    if ($actualHash -ne $expectedHash) { throw "ContextBridge download checksum mismatch." }
    Good "Download checksum verified."
    Expand-Archive -Path $archive -DestinationPath $temporary -Force
    New-Item -ItemType Directory -Force -Path $InstallDir | Out-Null
    Copy-Item (Join-Path $temporary "contextbridge.exe") (Join-Path $InstallDir "contextbridge.exe") -Force
    # Keep the unpacked extension's directory stable so a paired browser does
    # not lose its installation path during an in-place update.
    $extensionSource = Join-Path $temporary "extension"
    $extensionTarget = Join-Path $InstallDir "extension"
    New-Item -ItemType Directory -Path $extensionTarget -Force | Out-Null
    Get-ChildItem -LiteralPath $extensionSource -File -Recurse | ForEach-Object {
        $relative = [IO.Path]::GetRelativePath($extensionSource, $_.FullName)
        $destination = Join-Path $extensionTarget $relative
        New-Item -ItemType Directory -Path (Split-Path -Parent $destination) -Force | Out-Null
        Copy-Item -LiteralPath $_.FullName -Destination $destination -Force
    }
    Copy-Item (Join-Path $temporary "config.example.yml") (Join-Path $InstallDir "config.example.yml") -Force
} finally {
    $resolvedTemporary = [IO.Path]::GetFullPath($temporary)
    $tempRoot = [IO.Path]::GetFullPath([IO.Path]::GetTempPath()).TrimEnd([IO.Path]::DirectorySeparatorChar) + [IO.Path]::DirectorySeparatorChar
    if (-not $resolvedTemporary.StartsWith($tempRoot, [StringComparison]::OrdinalIgnoreCase) -or
        -not ([IO.Path]::GetFileName($resolvedTemporary) -match '^contextbridge-[0-9a-f]{32}$')) {
        throw "Refusing to remove an unexpected temporary directory: $resolvedTemporary"
    }
    Remove-Item -LiteralPath $temporary -Recurse -Force -ErrorAction SilentlyContinue
}

$exe = Join-Path $InstallDir "contextbridge.exe"
$config = Join-Path $InstallDir "config.yml"
if (-not $NoPath) {
    try {
        $resolvedInstallDir = [IO.Path]::GetFullPath($InstallDir).TrimEnd('\')
        $userPath = [Environment]::GetEnvironmentVariable("Path", "User")
        $pathEntries = @($userPath -split ';' | Where-Object { $_ })
        if (-not ($pathEntries | Where-Object { $_.TrimEnd('\') -ieq $resolvedInstallDir })) {
            $newUserPath = if ($userPath) { "$($userPath.TrimEnd(';'));$resolvedInstallDir" } else { $resolvedInstallDir }
            [Environment]::SetEnvironmentVariable("Path", $newUserPath, "User")
            Good "Added ContextBridge to your user PATH."
        }
        if (-not (($env:Path -split ';') | Where-Object { $_.TrimEnd('\') -ieq $resolvedInstallDir })) {
            $env:Path = "$resolvedInstallDir;$env:Path"
        }
    } catch {
        Muted "Could not update the user PATH. Run ContextBridge from $InstallDir or add that folder manually."
    }
}

# `contextbridge` remains the canonical executable. The short `cb` launcher is
# a tiny path-stable shim, so an in-place executable update cannot leave a stale
# copied cb.exe behind. Never replace an unrelated command or user-owned file.
$cbAlias = Join-Path $InstallDir 'cb.cmd'
$cbAliasMarker = ':: ContextBridge managed cb alias'
$cbAliasInstalled = $false
try {
    $writeAlias = $true
    if (Test-Path -LiteralPath $cbAlias) {
        $writeAlias = ([IO.File]::ReadAllText($cbAlias)).StartsWith($cbAliasMarker, [StringComparison]::Ordinal)
        if (-not $writeAlias) {
            Muted "Skipped the short 'cb' command because $cbAlias is not managed by ContextBridge."
        }
    } else {
        $existingCb = Get-Command cb -ErrorAction SilentlyContinue | Select-Object -First 1
        if ($existingCb) {
            $writeAlias = $false
            Muted "Skipped the short 'cb' command because it already belongs to $($existingCb.Source)."
        }
    }
    if ($writeAlias) {
        $aliasText = "$cbAliasMarker`r`n@echo off`r`n`"%~dp0contextbridge.exe`" %*`r`n"
        [IO.File]::WriteAllText($cbAlias, $aliasText, [Text.Encoding]::ASCII)
        $resolvedCb = Get-Command cb -ErrorAction SilentlyContinue | Select-Object -First 1
        $cbAliasInstalled = Test-ContextBridgeCommandPath -CommandInfo $resolvedCb -ExpectedPath $cbAlias
        if ($cbAliasInstalled) {
            Good "Commands ready: contextbridge and cb"
        } elseif ($resolvedCb) {
            Muted "The managed cb launcher remains at $cbAlias, but the active cb command belongs to $($resolvedCb.Source)."
        } else {
            Muted "The managed cb launcher is at $cbAlias but is not on PATH; use contextbridge or its full path."
        }
    }
} catch {
    Muted "Could not create the optional 'cb' command; use contextbridge."
}

if (-not $NoCompletion) {
    try {
        $completionPath = Join-Path $InstallDir 'contextbridge-completion.ps1'
        $completionText = (& $exe completion powershell | Out-String)
        if ($LASTEXITCODE -ne 0 -or -not $completionText.Trim()) {
            throw 'The ContextBridge completion generator returned no script.'
        }
        if (-not $cbAliasInstalled) {
            $completionText = $completionText.Replace('-CommandName contextbridge, cb', '-CommandName contextbridge')
        }
        [IO.File]::WriteAllText($completionPath, $completionText, (New-Object Text.UTF8Encoding($false)))

        Update-ContextBridgeCompletionProfile -ProfilePath $PROFILE.CurrentUserAllHosts -CompletionPath $completionPath
        $completionCommands = Get-ContextBridgeCompletionCommands -AliasInstalled $cbAliasInstalled
        Good "PowerShell completion installed for $completionCommands (open a new shell)."
    } catch {
        Muted "PowerShell completion could not be activated automatically. Run: contextbridge completion powershell"
    }
}
if (-not (Test-Path $config)) {
    & $exe init --config $config
}

if ($ClusterMode -eq "ask") {
    Write-Host ""
    Write-Host "Choose how this device participates:"
    Write-Host "  1) Local bridge only (recommended for a first install)"
    Write-Host "  2) Relay for other devices"
    Write-Host "  3) Worker for an existing relay"
    Write-Host "  4) Relay and worker on this device"
    $clusterChoice = Read-Host "Choose 1, 2, 3, or 4 [1]"
    $ClusterMode = if ($clusterChoice -eq "2") { "relay" } elseif ($clusterChoice -eq "3") { "worker" } elseif ($clusterChoice -eq "4") { "all" } else { "local" }
}
$clusterArguments = @("cluster", "configure", "--config", $config, "--mode", $ClusterMode)
if ($ClusterMode -in @("relay", "all")) { $clusterArguments += @("--listen", "auto") }
if ($ClusterMode -eq "worker") {
    if (-not $RelayUrl) { $RelayUrl = Read-Host "Public HTTPS relay URL" }
    if (-not $RelayUrl) { throw "A relay URL is required for worker mode." }
    $clusterArguments += @("--relay-url", $RelayUrl, "--name", $NodeName)
} elseif ($ClusterMode -eq "relay") {
    $publicUrl = Read-Host "Public HTTPS relay URL, or leave empty while configuring the reverse proxy"
    if ($publicUrl) { $clusterArguments += @("--public-url", $publicUrl) }
}
& $exe @clusterArguments
if ($LASTEXITCODE -ne 0) { throw "Cluster mode could not be configured." }
if ($ClusterMode -in @("worker", "all")) {
    & $exe pair --config $config
    if ($LASTEXITCODE -ne 0) { throw "Worker pairing did not complete." }
}

if ($Provider -eq "browser") {
    $yaml = Get-Content $config -Raw
    $yaml = [regex]::Replace($yaml, '(?ms)(^  (?:default|inkwall):\r?\n(?:(?!^  [A-Za-z0-9_-]+:).)*?^    provider:\s*)ollama\s*$', '${1}browser')
    $yaml = [regex]::Replace($yaml, '(?ms)(^  (?:default|inkwall):\r?\n(?:(?!^  [A-Za-z0-9_-]+:).)*?^    fallback:\s*)\[browser\]\s*$', '${1}[]')
    [IO.File]::WriteAllText($config, $yaml, (New-Object Text.UTF8Encoding($false)))
} elseif ($Provider -eq "ollama") {
    $yaml = Get-Content $config -Raw
    $yaml = [regex]::Replace($yaml, '(?ms)(^  (?:default|inkwall):\r?\n(?:(?!^  [A-Za-z0-9_-]+:).)*?^    provider:\s*)browser\s*$', '${1}ollama')
    $yaml = [regex]::Replace($yaml, '(?ms)(^  (?:default|inkwall):\r?\n(?:(?!^  [A-Za-z0-9_-]+:).)*?^    fallback:\s*)\[\]\s*$', '${1}[browser]')
    [IO.File]::WriteAllText($config, $yaml, (New-Object Text.UTF8Encoding($false)))
} elseif ($Provider -eq "managed") {
    Info "Installing the verified llama.cpp runtime..."
    & $exe runtime install --config $config llama.cpp
    if ($LASTEXITCODE -ne 0) { throw "The llama.cpp runtime could not be installed." }
    $yaml = Get-Content $config -Raw
    $models = @()
    if ($ManagedModel -in @("jina", "both")) {
        $models += "jina-v4-retrieval"
        $yaml = [regex]::Replace($yaml, '(?ms)(^  jina:\r?\n(?:(?!^  [A-Za-z0-9_-]+:).)*?^    auto_start:\s*)false', '${1}true')
    }
    if ($ManagedModel -in @("nuextract", "both")) {
        $models += "nuextract3"
        $yaml = [regex]::Replace($yaml, '(?ms)(^  nuextract:\r?\n(?:(?!^  [A-Za-z0-9_-]+:).)*?^    auto_start:\s*)false', '${1}true')
    }
    [IO.File]::WriteAllText($config, $yaml, (New-Object Text.UTF8Encoding($false)))
    foreach ($model in $models) {
        Info "Downloading and verifying $model..."
        & $exe pull --config $config $model
        if ($LASTEXITCODE -ne 0) { throw "Model $model could not be installed." }
    }
}

if (-not $NoAutostart -and (Get-Command Register-ScheduledTask -ErrorAction SilentlyContinue)) {
    $taskName = "ContextBridge"
    $arguments = "run --config `"$config`""
    $action = New-ScheduledTaskAction -Execute $exe -Argument $arguments -WorkingDirectory $InstallDir
    $trigger = New-ScheduledTaskTrigger -AtLogOn -User $env:USERNAME
    $settings = New-ScheduledTaskSettingsSet -RestartCount 5 -RestartInterval (New-TimeSpan -Minutes 1) -ExecutionTimeLimit (New-TimeSpan -Days 3650) -AllowStartIfOnBatteries -DontStopIfGoingOnBatteries
    Register-ScheduledTask -TaskName $taskName -Action $action -Trigger $trigger -Settings $settings -Description "Local ContextBridge service" -Force | Out-Null
    $updateAction = New-ScheduledTaskAction -Execute $exe -Argument "update auto --config `"$config`"" -WorkingDirectory $InstallDir
    $updateTrigger = New-ScheduledTaskTrigger -Daily -At "03:00"
    $updateSettings = New-ScheduledTaskSettingsSet -StartWhenAvailable -RestartCount 3 -RestartInterval (New-TimeSpan -Minutes 5) -AllowStartIfOnBatteries -DontStopIfGoingOnBatteries
    Register-ScheduledTask -TaskName "ContextBridge Update" -Action $updateAction -Trigger $updateTrigger -Settings $updateSettings -Description "Verified ContextBridge automatic updater" -Force | Out-Null
    Good "Autostart installed for this Windows account."
} elseif (-not $NoAutostart) {
    Muted "Windows Task Scheduler cmdlets are unavailable. Start ContextBridge manually when needed."
}

try {
    $programs = [Environment]::GetFolderPath('Programs')
    if ($programs) {
        $menuDir = Join-Path $programs 'ContextBridge'
        New-Item -ItemType Directory -Path $menuDir -Force | Out-Null
        $shell = New-Object -ComObject WScript.Shell
        foreach ($entry in @(
            @{ Name = 'Terminal'; Command = 'console'; Description = 'View the running ContextBridge service without starting another one' },
            @{ Name = 'Dashboard'; Command = 'dashboard'; Description = 'Open the local ContextBridge dashboard' }
        )) {
            $shortcut = $shell.CreateShortcut((Join-Path $menuDir ("$($entry.Name).lnk")))
            $shortcut.TargetPath = $exe
            $shortcut.Arguments = "$($entry.Command) --config `"$config`""
            $shortcut.WorkingDirectory = $InstallDir
            $shortcut.Description = $entry.Description
            $shortcut.Save()
        }
        Good 'Start menu: ContextBridge > Terminal / Dashboard'
    }
} catch {
    Muted 'Start menu shortcuts could not be created; use contextbridge console or contextbridge dashboard.'
}

if (-not $NoStart) {
    $started = $null
    $existing = Get-Process contextbridge -ErrorAction SilentlyContinue | Where-Object { $_.Path -eq $exe }
    if (-not $existing) {
        $started = Start-Process -FilePath $exe -ArgumentList @("run", "--config", $config) -WindowStyle Hidden -PassThru
    }
    $listenLine = Get-Content $config | Where-Object { $_ -match '^\s{4}listen:\s*(\S+)\s*$' } | Select-Object -First 1
    $listenAddress = if ($listenLine -match '^\s{4}listen:\s*(\S+)\s*$') { $Matches[1] } else { '127.0.0.1:32145' }
    $ready = $false
    for ($attempt = 0; $attempt -lt 30; $attempt++) {
        try {
            $health = Invoke-RestMethod -Uri "http://$listenAddress/health" -TimeoutSec 2
            if ($health.ok) { $ready = $true; break }
        } catch {}
        if ($started -and $started.HasExited) { break }
        Start-Sleep -Milliseconds 500
    }
    if (-not $ready) {
        throw "ContextBridge was installed but did not become healthy. Run: `"$exe`" doctor --config `"$config`""
    }
}

Write-Host ""
Good "ContextBridge is installed."
Write-Host "Config: $config"
Write-Host "Chromium extension: $(Join-Path $InstallDir 'extension\chromium')"
Write-Host "Firefox extension: $(Join-Path $InstallDir 'extension\firefox')"
if ($Provider -eq "browser") {
    Write-Host "Load the Chromium folder in Chrome, Edge, Opera, Brave, or Vivaldi."
    Write-Host "Load the Firefox manifest from about:debugging for local use."
    $tokenLine = Get-Content $config | Where-Object { $_ -match '^\s*token:\s*(\S+)\s*$' } | Select-Object -First 1
    if ($tokenLine -match '^\s*token:\s*(\S+)\s*$' -and (Get-Command Set-Clipboard -ErrorAction SilentlyContinue)) {
        Set-Clipboard -Value $Matches[1]
        Good "The local pairing token is already in your clipboard."
    }
}
if (-not $NoStart -and -not $NoDashboard) {
    Start-Sleep -Milliseconds 700
    & $exe dashboard --config $config
}
