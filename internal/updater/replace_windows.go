//go:build windows

package updater

import (
	"encoding/base64"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
)

func replaceExecutable(current, staged, expectedVersion, configPath, healthURL, failurePath string) (bool, error) {
	next := current + ".next.exe"
	backup := current + ".previous.exe"
	script := current + ".update.ps1"
	_ = os.Remove(next)
	if err := copyFile(staged, next, 0700); err != nil {
		return false, err
	}
	if err := validateExecutable(next, expectedVersion, configPath); err != nil {
		_ = os.Remove(next)
		return false, err
	}
	body := `$ErrorActionPreference = "Stop"
$current = $args[0]
$next = $args[1]
$backup = $args[2]
$expected = $args[3]
$parentPid = [int]$args[4]
$restartLine = [Text.Encoding]::UTF8.GetString([Convert]::FromBase64String($args[5]))
$healthUrl = $args[6]
$failurePath = $args[7]
$maxProbes = [int]$args[8]
try { Wait-Process -Id $parentPid -Timeout 60 -ErrorAction SilentlyContinue } catch {}
$managedTask = Get-ScheduledTask -TaskName "ContextBridge" -ErrorAction SilentlyContinue
if ($managedTask -and ($managedTask.State -ne 'Running' -or $managedTask.Actions[0].Execute -ne $current)) { $managedTask = $null }
$manualProcessRunning = $false
if (-not $managedTask -and -not $restartLine -and $healthUrl) {
  try {
    $existing = Invoke-RestMethod -Uri $healthUrl -TimeoutSec 2
    $manualProcessRunning = $existing.ok -eq $true -and $existing.version -ne $expected
  } catch {}
}
if ($manualProcessRunning) {
  $failure = @{ version = $expected; error = "Manual update needs the running ContextBridge terminal to exit first. Close it with Ctrl+C, then run update apply again; the previous version is still running."; at = [DateTime]::UtcNow.ToString('o') }
  [IO.File]::WriteAllText($failurePath, ($failure | ConvertTo-Json -Compress), (New-Object Text.UTF8Encoding($false)))
  exit 1
}
if ($managedTask) {
  Stop-ScheduledTask -TaskName "ContextBridge" -ErrorAction SilentlyContinue
  Start-Sleep -Seconds 1
}
$startedProcess = $null
function Start-Managed {
  if ($managedTask) { Start-ScheduledTask -TaskName "ContextBridge" }
  elseif ($restartLine) { $script:startedProcess = Start-Process -FilePath $current -ArgumentList $restartLine -WindowStyle Hidden -PassThru }
}
function Test-Healthy {
  # A manual CLI update with no managed task has no service to restart here.
  # The staged executable was validated above; the user starts it explicitly.
  if (-not $managedTask -and -not $restartLine) { return $true }
  if (-not $healthUrl) {
    Start-Sleep -Seconds 10
    if ($managedTask) { return (Get-ScheduledTask -TaskName "ContextBridge").State -eq 'Running' }
    return $startedProcess -and -not $startedProcess.HasExited
  }
  for ($probe = 0; $probe -lt $maxProbes; $probe++) {
    try {
      $health = Invoke-RestMethod -Uri $healthUrl -TimeoutSec 2
      if ($health.ok -eq $true -and $health.version -eq $expected) { return $true }
    } catch {}
    Start-Sleep -Seconds 1
  }
  return $false
}
$installed = $false
$failureReason = "Update $expected failed validation or health check; previous version restored."
if (Test-Path $backup) { Remove-Item $backup -Force }
for ($attempt = 0; $attempt -lt 30; $attempt++) {
  try {
    if (-not (Test-Path $current) -and (Test-Path $backup)) { Move-Item $backup $current -Force }
    Move-Item $current $backup
    Move-Item $next $current
    $installed = $true
    break
  } catch {
    if ((Test-Path $backup) -and -not (Test-Path $current)) { Move-Item $backup $current -Force -ErrorAction SilentlyContinue }
    Start-Sleep -Seconds 1
  }
}
if ($installed) {
  try {
    $reported = (& $current version | Out-String).Trim()
    if ($LASTEXITCODE -ne 0 -or $reported -ne $expected) { throw 'Updated executable validation failed.' }
    Start-Managed
    if (-not (Test-Healthy)) { throw 'Updated service did not become healthy.' }
    Remove-Item $failurePath -Force -ErrorAction SilentlyContinue
    exit 0
  } catch {}
}
if (-not $installed) { $failureReason = "Could not replace the ContextBridge executable after 30 attempts; it may still be in use or access may be denied. Close the running terminal and retry." }
if ($managedTask) { Stop-ScheduledTask -TaskName "ContextBridge" -ErrorAction SilentlyContinue }
if ($startedProcess -and -not $startedProcess.HasExited) { Stop-Process -Id $startedProcess.Id -Force -ErrorAction SilentlyContinue }
if (Test-Path $backup) {
  if (Test-Path $current) {
    $failed = $current + '.failed.exe'
    Remove-Item $failed -Force -ErrorAction SilentlyContinue
    Move-Item $current $failed -Force -ErrorAction SilentlyContinue
  }
  if (-not (Test-Path $current)) { Move-Item $backup $current -Force -ErrorAction SilentlyContinue }
}
$failure = @{ version = $expected; error = $failureReason; at = [DateTime]::UtcNow.ToString('o') }
[IO.File]::WriteAllText($failurePath, ($failure | ConvertTo-Json -Compress), (New-Object Text.UTF8Encoding($false)))
if (Test-Path $current) { Start-Managed }
exit 1
`
	if err := os.WriteFile(script, []byte(strings.ReplaceAll(body, "\n", "\r\n")), 0600); err != nil {
		_ = os.Remove(next)
		return false, err
	}
	restartLine := ""
	if len(os.Args) > 1 && (os.Args[1] == "run" || os.Args[1] == "serve") {
		quoted := make([]string, 0, len(os.Args)-1)
		for _, argument := range os.Args[1:] {
			quoted = append(quoted, syscall.EscapeArg(argument))
		}
		restartLine = strings.Join(quoted, " ")
	}
	encodedRestart := base64.StdEncoding.EncodeToString([]byte(restartLine))
	command := exec.Command("powershell.exe", "-NoLogo", "-NoProfile", "-NonInteractive", "-ExecutionPolicy", "Bypass", "-File", script, current, next, backup, expectedVersion, strconv.Itoa(os.Getpid()), encodedRestart, healthURL, failurePath, "45")
	command.Dir = filepath.Dir(current)
	if err := command.Start(); err != nil {
		_ = os.Remove(next)
		return false, fmt.Errorf("start update helper: %w", err)
	}
	return true, nil
}

func copyFile(source, destination string, mode os.FileMode) error {
	input, err := os.Open(source)
	if err != nil {
		return err
	}
	defer input.Close()
	output, err := os.OpenFile(destination, os.O_CREATE|os.O_EXCL|os.O_WRONLY, mode)
	if err != nil {
		return err
	}
	if _, err := output.ReadFrom(input); err != nil {
		output.Close()
		_ = os.Remove(destination)
		return err
	}
	return output.Close()
}
