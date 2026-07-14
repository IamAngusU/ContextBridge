param(
    [string]$InstallDir = "$env:LOCALAPPDATA\ContextBridge",
    [ValidateSet("ask", "ollama", "browser", "later")][string]$Provider = "ask",
    [switch]$NoAutostart,
    [switch]$NoStart
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
    Write-Host "  1) Ollama, with the browser as fallback"
    Write-Host "  2) A paired browser tab"
    Write-Host "  3) Configure it later in YAML"
    $choice = Read-Host "Choose 1, 2, or 3 [1]"
    if (-not $choice) { $choice = "1" }
    $Provider = if ($choice -eq "2") { "browser" } elseif ($choice -eq "3") { "later" } else { "ollama" }
}

$architecture = if ($env:PROCESSOR_ARCHITECTURE -eq "ARM64") { "arm64" } else { "amd64" }
$release = Invoke-RestMethod -Uri "https://api.github.com/repos/$repo/releases/latest" -Headers @{ "User-Agent" = "ContextBridge-Installer" }
$assetName = "contextbridge_windows_$architecture.zip"
$asset = $release.assets | Where-Object { $_.name -eq $assetName } | Select-Object -First 1
if (-not $asset) { throw "Release asset $assetName was not found." }

$temporary = Join-Path ([IO.Path]::GetTempPath()) ("contextbridge-" + [guid]::NewGuid().ToString("N"))
New-Item -ItemType Directory -Force -Path $temporary | Out-Null
try {
    Info "Downloading $($release.tag_name) for Windows $architecture..."
    $archive = Join-Path $temporary $assetName
    Invoke-WebRequest -Uri $asset.browser_download_url -OutFile $archive -UseBasicParsing
    Expand-Archive -Path $archive -DestinationPath $temporary -Force
    New-Item -ItemType Directory -Force -Path $InstallDir | Out-Null
    Copy-Item (Join-Path $temporary "contextbridge.exe") (Join-Path $InstallDir "contextbridge.exe") -Force
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
    $yaml = $yaml -replace '(?m)^\s{4}provider: ollama\s*$', '    provider: browser'
    $yaml = $yaml -replace '(?m)^\s{4}fallback: \[browser\]\s*$', '    fallback: []'
    [IO.File]::WriteAllText($config, $yaml, (New-Object Text.UTF8Encoding($false)))
} elseif ($Provider -eq "ollama") {
    $yaml = Get-Content $config -Raw
    $yaml = $yaml -replace '(?m)^\s{4}provider: browser\s*$', '    provider: ollama'
    $yaml = $yaml -replace '(?m)^\s{4}fallback: \[\]\s*$', '    fallback: [browser]'
    [IO.File]::WriteAllText($config, $yaml, (New-Object Text.UTF8Encoding($false)))
}

if (-not $NoAutostart) {
    $taskName = "ContextBridge"
    $arguments = "serve --config `"$config`""
    $action = New-ScheduledTaskAction -Execute $exe -Argument $arguments
    $trigger = New-ScheduledTaskTrigger -AtLogOn -User $env:USERNAME
    $settings = New-ScheduledTaskSettingsSet -RestartCount 5 -RestartInterval (New-TimeSpan -Minutes 1) -ExecutionTimeLimit (New-TimeSpan -Days 3650)
    Register-ScheduledTask -TaskName $taskName -Action $action -Trigger $trigger -Settings $settings -Description "Local ContextBridge service" -Force | Out-Null
    Good "Autostart installed for this Windows account."
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
Write-Host "Browser extension: $(Join-Path $InstallDir 'extension')"
if ($Provider -eq "browser") {
    Write-Host "Open chrome://extensions or edge://extensions, enable developer mode, and load that extension folder."
}
