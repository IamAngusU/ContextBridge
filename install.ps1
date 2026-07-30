param(
    [string]$InstallDir = "$env:LOCALAPPDATA\ContextBridge",
    [ValidateSet("ask", "ollama", "managed", "browser", "later")][string]$Provider = "ask",
    [ValidateSet("jina", "nuextract", "both")][string]$ManagedModel = "jina",
    [switch]$NoAutostart,
    [switch]$NoStart,
    [switch]$NoDashboard
)

$ErrorActionPreference = "Stop"
$repo = "IamAngusU/ContextBridge"

function Info($Text) { Write-Host $Text -ForegroundColor Cyan }
function Good($Text) { Write-Host $Text -ForegroundColor Green }
function Muted($Text) { Write-Host $Text -ForegroundColor DarkGray }

Info "ContextBridge installer"
Muted "A local bridge for Ollama and explicitly paired browser tabs."

if ($Provider -eq "ask") {
    Write-Host ""
    Write-Host "Choose the first local target:"
    Write-Host "  1) Existing Ollama, with automatic local model detection (recommended)"
    Write-Host "  2) Managed llama.cpp runtime and a verified GGUF model"
    Write-Host "  3) A visually taught browser tab"
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
    if (Test-Path (Join-Path $InstallDir "extension")) {
        Remove-Item (Join-Path $InstallDir "extension") -Recurse -Force
    }
    Copy-Item (Join-Path $temporary "extension") (Join-Path $InstallDir "extension") -Recurse -Force
    Copy-Item (Join-Path $temporary "config.example.yml") (Join-Path $InstallDir "config.example.yml") -Force
} finally {
    Remove-Item $temporary -Recurse -Force -ErrorAction SilentlyContinue
}

$exe = Join-Path $InstallDir "contextbridge.exe"
$config = Join-Path $InstallDir "config.yml"
if (-not (Test-Path $config)) {
    & $exe init --config $config
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
    $arguments = "serve --config `"$config`""
    $action = New-ScheduledTaskAction -Execute $exe -Argument $arguments
    $trigger = New-ScheduledTaskTrigger -AtLogOn -User $env:USERNAME
    $settings = New-ScheduledTaskSettingsSet -RestartCount 5 -RestartInterval (New-TimeSpan -Minutes 1) -ExecutionTimeLimit (New-TimeSpan -Days 3650)
    Register-ScheduledTask -TaskName $taskName -Action $action -Trigger $trigger -Settings $settings -Description "Local ContextBridge service" -Force | Out-Null
    Good "Autostart installed for this Windows account."
} elseif (-not $NoAutostart) {
    Muted "Windows Task Scheduler cmdlets are unavailable. Start ContextBridge manually when needed."
}

if (-not $NoStart) {
    $existing = Get-Process contextbridge -ErrorAction SilentlyContinue
    if (-not $existing) {
        Start-Process -FilePath $exe -ArgumentList @("serve", "--config", $config) -WindowStyle Hidden
        Start-Sleep -Milliseconds 600
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
