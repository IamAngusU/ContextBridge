param(
    [string]$InstallDir = "$env:LOCALAPPDATA\ContextBridge",
    [ValidateSet("ask", "ollama", "managed", "later")][string]$Provider = "ask",
    [ValidateSet("ask", "local", "relay", "worker", "all")][string]$ClusterMode = "ask",
    [ValidateSet("jina", "nuextract", "both")][string]$ManagedModel = "jina",
    [string]$RelayUrl = "",
    [string]$NodeName = "auto",
    [string]$CommandName = "",
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
    param([Parameter(Mandatory = $true)][string[]]$Commands)
    if ($Commands.Count -eq 0) { return '' }
    if ($Commands.Count -eq 1) { return $Commands[0] }
    return (($Commands[0..($Commands.Count - 2)] -join ', ') + ' and ' + $Commands[-1])
}

function Write-ContextBridgeInstallManifest {
    param(
        [Parameter(Mandatory = $true)][string]$Root,
        [Parameter(Mandatory = $true)][string[]]$OwnedPaths
    )
    $resolvedRoot = [IO.Path]::GetFullPath($Root).TrimEnd('\')
    $rootPrefix = $resolvedRoot + '\'
    $normalized = @()
    foreach ($relative in @($OwnedPaths | Sort-Object -Unique)) {
        if (-not $relative -or [IO.Path]::IsPathRooted($relative) -or $relative.Contains('\')) {
            throw "Unsafe installer ownership path: $relative"
        }
        $target = [IO.Path]::GetFullPath((Join-Path $resolvedRoot $relative))
        if (-not $target.StartsWith($rootPrefix, [StringComparison]::OrdinalIgnoreCase)) {
            throw "Installer ownership path escaped the installation: $relative"
        }
        $normalized += $relative
    }
    $manifest = [ordered]@{
        schema_version = 1
        product = 'ContextBridge'
        paths = @($normalized)
    } | ConvertTo-Json -Depth 3
    $manifestPath = Join-Path $resolvedRoot '.contextbridge-install.json'
    $stagePath = Join-Path $resolvedRoot ('.contextbridge-install.' + [guid]::NewGuid().ToString('N') + '.tmp')
    try {
        [IO.File]::WriteAllText($stagePath, ($manifest + "`n"), (New-Object Text.UTF8Encoding($false)))
        Move-Item -LiteralPath $stagePath -Destination $manifestPath -Force
    } finally {
        Remove-Item -LiteralPath $stagePath -Force -ErrorAction SilentlyContinue
    }
}

function Test-ContextBridgeCommandName {
    param([string]$Name)
    return $Name -match '^[A-Za-z][A-Za-z0-9_-]{0,31}$'
}

function Test-ContextBridgeInstallCommand {
    param($CommandInfo)
    if (-not $CommandInfo -or -not $CommandInfo.Path -or ([IO.Path]::GetFileName([string]$CommandInfo.Path) -ine 'contextbridge.exe')) { return $false }
    try {
        $directory = [IO.Path]::GetDirectoryName([IO.Path]::GetFullPath([string]$CommandInfo.Path))
        return (Test-Path -LiteralPath (Join-Path $directory 'config.yml') -PathType Leaf)
    } catch {
        return $false
    }
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
Muted "A local-first router for local engines, APIs, and trusted worker pools."

$existingCanonicalCommand = Get-Command contextbridge -ErrorAction SilentlyContinue | Select-Object -First 1
if (-not $PSBoundParameters.ContainsKey('InstallDir') -and (Test-ContextBridgeInstallCommand $existingCanonicalCommand)) {
    $InstallDir = Split-Path -Parent $existingCanonicalCommand.Path
    Muted "Updating the existing ContextBridge installation at $InstallDir."
}
$expectedCanonicalCommand = Join-Path $InstallDir 'contextbridge.exe'
$expectedCbCommand = Join-Path $InstallDir 'cb.cmd'
$existingCbCommand = Get-Command cb -ErrorAction SilentlyContinue | Select-Object -First 1
$canonicalNameAvailable = (-not $existingCanonicalCommand) -or (Test-ContextBridgeCommandPath -CommandInfo $existingCanonicalCommand -ExpectedPath $expectedCanonicalCommand)
$cbNameAvailable = (-not $existingCbCommand) -or (Test-ContextBridgeCommandPath -CommandInfo $existingCbCommand -ExpectedPath $expectedCbCommand)

if ($CommandName -and (($CommandName -ieq 'contextbridge') -or ($CommandName -ieq 'cb') -or -not (Test-ContextBridgeCommandName $CommandName))) {
    throw '-CommandName must be a custom shell name with 1-32 letters, numbers, underscores, or hyphens, starting with a letter.'
}
if (-not $NoPath -and -not $CommandName -and -not $canonicalNameAvailable -and -not $cbNameAvailable) {
    $canPrompt = [Environment]::UserInteractive -and -not [Console]::IsInputRedirected
    if (-not $canPrompt) {
        throw "Both 'contextbridge' and 'cb' already belong to other programs. Re-run with -CommandName <your-name>."
    }
    for ($attempt = 0; $attempt -lt 3 -and -not $CommandName; $attempt++) {
        $candidate = (Read-Host "Both 'contextbridge' and 'cb' are taken. Choose a command name for ContextBridge").Trim()
        if (-not (Test-ContextBridgeCommandName $candidate) -or $candidate -ieq 'contextbridge' -or $candidate -ieq 'cb') {
            Muted 'Use 1-32 letters, numbers, underscores, or hyphens, starting with a letter.'
            continue
        }
        $candidatePath = Join-Path $InstallDir ($candidate + '.cmd')
        $candidateCommand = Get-Command $candidate -ErrorAction SilentlyContinue | Select-Object -First 1
        if ($candidateCommand -and -not (Test-ContextBridgeCommandPath -CommandInfo $candidateCommand -ExpectedPath $candidatePath)) {
            Muted "'$candidate' is already taken. Choose another name."
            continue
        }
        $CommandName = $candidate
    }
    if (-not $CommandName) { throw 'No safe ContextBridge command name was selected.' }
}
if ($CommandName) {
    $expectedCustomCommand = Join-Path $InstallDir ($CommandName + '.cmd')
    $existingCustomCommand = Get-Command $CommandName -ErrorAction SilentlyContinue | Select-Object -First 1
    if ($existingCustomCommand -and -not (Test-ContextBridgeCommandPath -CommandInfo $existingCustomCommand -ExpectedPath $expectedCustomCommand)) {
        throw "The requested command '$CommandName' already belongs to another program at $($existingCustomCommand.Source)."
    }
}

if ($Provider -eq "ask") {
    Write-Host ""
    Write-Host "Choose the first local target:"
    Write-Host "  1) Existing Ollama, with automatic local model detection (recommended)"
    Write-Host "  2) Managed llama.cpp runtime and a verified GGUF model"
    Write-Host "  3) Configure it later in YAML"
    $choice = Read-Host "Choose 1, 2, or 3 [1]"
    if (-not $choice) { $choice = "1" }
    $Provider = if ($choice -eq "2") { "managed" } elseif ($choice -eq "3") { "later" } else { "ollama" }
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

$canonicalCommandInstalled = $false
if (-not $NoPath -and $canonicalNameAvailable) {
    $canonicalCommandInstalled = Test-ContextBridgeCommandPath -CommandInfo (Get-Command contextbridge -ErrorAction SilentlyContinue | Select-Object -First 1) -ExpectedPath $exe
}

# `contextbridge` remains the canonical executable. The short `cb` launcher is
# a tiny path-stable shim, so an in-place executable update cannot leave a stale
# copied cb.exe behind. Never replace an unrelated command or user-owned file.
$cbAlias = Join-Path $InstallDir 'cb.cmd'
$cbAliasMarker = ':: ContextBridge managed cb alias'
$cbAliasInstalled = $false
try {
    $writeAlias = $cbNameAvailable
    if (-not $writeAlias) {
        Muted "Skipped the short 'cb' command because it already belongs to $($existingCbCommand.Source)."
    }
    if (Test-Path -LiteralPath $cbAlias) {
        $ownedAlias = ([IO.File]::ReadAllText($cbAlias)).StartsWith($cbAliasMarker, [StringComparison]::Ordinal)
        $writeAlias = $writeAlias -and $ownedAlias
        if (-not $ownedAlias) {
            Muted "Skipped the short 'cb' command because $cbAlias is not managed by ContextBridge."
        }
    } elseif ($writeAlias) {
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
        if (-not $cbAliasInstalled -and $resolvedCb) {
            Muted "The managed cb launcher remains at $cbAlias, but the active cb command belongs to $($resolvedCb.Source)."
        } elseif (-not $cbAliasInstalled) {
            Muted "The managed cb launcher is at $cbAlias but is not on PATH; use contextbridge or its full path."
        }
    }
} catch {
    Muted "Could not create the optional 'cb' command; use contextbridge."
}

$customCommandInstalled = $false
if ($CommandName) {
    $customAlias = Join-Path $InstallDir ($CommandName + '.cmd')
    $customAliasMarker = ':: ContextBridge managed custom command'
    if ((Test-Path -LiteralPath $customAlias) -and -not ([IO.File]::ReadAllText($customAlias)).StartsWith($customAliasMarker, [StringComparison]::Ordinal)) {
        throw "Refusing to replace the user-owned command file $customAlias."
    }
    $customAliasText = "$customAliasMarker`r`n@echo off`r`n`"%~dp0contextbridge.exe`" %*`r`n"
    [IO.File]::WriteAllText($customAlias, $customAliasText, [Text.Encoding]::ASCII)
    if ($NoPath) {
        Muted "The custom launcher is at $customAlias but was intentionally not added to PATH."
    } else {
        $customCommandInstalled = Test-ContextBridgeCommandPath -CommandInfo (Get-Command $CommandName -ErrorAction SilentlyContinue | Select-Object -First 1) -ExpectedPath $customAlias
        if (-not $customCommandInstalled) {
            throw "The custom ContextBridge command '$CommandName' was created but is shadowed by another command."
        }
    }
}

$completionCommandNames = @()
if ($canonicalCommandInstalled) { $completionCommandNames += 'contextbridge' }
if ($cbAliasInstalled) { $completionCommandNames += 'cb' }
if ($customCommandInstalled) { $completionCommandNames += $CommandName }
$preferredCommand = if ($customCommandInstalled) { $CommandName } elseif ($cbAliasInstalled -and -not $canonicalCommandInstalled) { 'cb' } elseif ($canonicalCommandInstalled) { 'contextbridge' } else { '"' + $exe + '"' }
if ($completionCommandNames.Count -gt 0) {
    Good "Commands ready: $(Get-ContextBridgeCompletionCommands -Commands $completionCommandNames)"
} else {
    Muted "No unshadowed command name is on PATH; run $exe directly or reinstall with -CommandName <your-name>."
}

if (-not $NoCompletion -and $completionCommandNames.Count -gt 0) {
    try {
        $completionPath = Join-Path $InstallDir 'contextbridge-completion.ps1'
        $completionText = (& $exe completion powershell | Out-String)
        if ($LASTEXITCODE -ne 0 -or -not $completionText.Trim()) {
            throw 'The ContextBridge completion generator returned no script.'
        }
        $generatedCommandList = '-CommandName contextbridge, cb'
        if (-not $completionText.Contains($generatedCommandList)) {
            throw 'The ContextBridge completion generator returned an unexpected command registration.'
        }
        $completionText = $completionText.Replace($generatedCommandList, '-CommandName ' + ($completionCommandNames -join ', '))
        [IO.File]::WriteAllText($completionPath, $completionText, (New-Object Text.UTF8Encoding($false)))

        Update-ContextBridgeCompletionProfile -ProfilePath $PROFILE.CurrentUserAllHosts -CompletionPath $completionPath
        $completionCommands = Get-ContextBridgeCompletionCommands -Commands $completionCommandNames
        Good "PowerShell completion installed for $completionCommands (open a new shell)."
    } catch {
        Muted "PowerShell completion could not be activated automatically. Run: $preferredCommand completion powershell"
    }
}

$ownedInstallPaths = @('contextbridge.exe', 'config.example.yml')
foreach ($candidate in @(Get-ChildItem -LiteralPath $InstallDir -File -Filter '*.cmd' -ErrorAction SilentlyContinue)) {
    $text = [IO.File]::ReadAllText($candidate.FullName)
    if ($text.StartsWith(':: ContextBridge managed cb alias', [StringComparison]::Ordinal) -or
        $text.StartsWith(':: ContextBridge managed custom command', [StringComparison]::Ordinal)) {
        $ownedInstallPaths += $candidate.Name
    }
}
if (Test-Path -LiteralPath (Join-Path $InstallDir 'contextbridge-completion.ps1') -PathType Leaf) {
    $ownedInstallPaths += 'contextbridge-completion.ps1'
}
Write-ContextBridgeInstallManifest -Root $InstallDir -OwnedPaths $ownedInstallPaths

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

if ($Provider -eq "managed") {
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
    Muted "Start menu shortcuts could not be created; use $preferredCommand console or $preferredCommand dashboard."
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
if (-not $NoStart -and -not $NoDashboard) {
    Start-Sleep -Milliseconds 700
    & $exe dashboard --config $config
}
