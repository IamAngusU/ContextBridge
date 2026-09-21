//go:build windows

package main

import (
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"syscall"
)

func addPlatformUninstallPlan(plan *uninstallPlan) error {
	return nil
}

func executeUninstall(plan uninstallPlan) error {
	directory, err := os.MkdirTemp("", "contextbridge-uninstall-")
	if err != nil {
		return fmt.Errorf("create uninstall handoff: %w", err)
	}
	planPath := filepath.Join(directory, "plan.json")
	scriptPath := filepath.Join(directory, "uninstall.ps1")
	plan.VerificationLog = filepath.Join(directory, "verification.log")
	raw, err := json.Marshal(plan)
	if err != nil {
		return err
	}
	if err := os.WriteFile(planPath, raw, 0600); err != nil {
		return fmt.Errorf("write uninstall plan: %w", err)
	}
	if err := os.WriteFile(scriptPath, []byte(windowsUninstallHelper), 0600); err != nil {
		return fmt.Errorf("write uninstall helper: %w", err)
	}
	if err := startWindowsUninstallHelper(scriptPath, planPath, os.Getpid()); err != nil {
		return fmt.Errorf("start uninstall handoff: %w", err)
	}
	fmt.Println("ContextBridge uninstall scheduled. This process will exit so Windows can remove the executable.")
	fmt.Printf("If removal cannot be verified, details will remain at %s.\n", plan.VerificationLog)
	if plan.Purge {
		fmt.Println("Locally managed configuration and data from the displayed plan will also be removed.")
	}
	return nil
}

func startWindowsUninstallHelper(scriptPath, planPath string, parentPID int) error {
	arguments := []string{"-NoLogo", "-NoProfile", "-NonInteractive", "-ExecutionPolicy", "Bypass", "-File", scriptPath, "-PlanPath", planPath, "-ParentPID", strconv.Itoa(parentPID)}
	// A terminal or service manager can place ContextBridge in a kill-on-close
	// Windows job. Prefer an explicit breakaway so the handoff survives long
	// enough to remove the executable after this process exits. Some restricted
	// jobs forbid breakaway, so retain a no-window fallback for ordinary shells.
	const (
		createNewProcessGroup  = 0x00000200
		createBreakawayFromJob = 0x01000000
		createNoWindow         = 0x08000000
	)
	var lastErr error
	for _, creationFlags := range []uint32{
		createNewProcessGroup | createBreakawayFromJob | createNoWindow,
		createNewProcessGroup | createNoWindow,
	} {
		command := exec.Command("powershell.exe", arguments...)
		command.SysProcAttr = &syscall.SysProcAttr{HideWindow: true, CreationFlags: creationFlags}
		if err := command.Start(); err == nil {
			return nil
		} else {
			lastErr = err
		}
	}
	return lastErr
}

const windowsUninstallHelper = `param(
  [Parameter(Mandatory=$true)][string]$PlanPath,
  [Parameter(Mandatory=$true)][int]$ParentPID
)
$ErrorActionPreference = 'SilentlyContinue'
$plan = Get-Content -LiteralPath $PlanPath -Raw | ConvertFrom-Json

function FullPath([string]$Value) {
  if (-not $Value) { return '' }
  try { return [IO.Path]::GetFullPath($Value).TrimEnd('\') } catch { return '' }
}
function SamePath([string]$Left, [string]$Right) {
  return (FullPath $Left).Equals((FullPath $Right), [StringComparison]::OrdinalIgnoreCase)
}

$ownedExecutables = @($plan.install_binary, $plan.current_binary) | ForEach-Object { FullPath $_ } | Where-Object { $_ }
$ownedTaskNames = @()
foreach ($taskName in @('ContextBridge', 'ContextBridge Update')) {
  $task = Get-ScheduledTask -TaskName $taskName -ErrorAction SilentlyContinue
  if (-not $task) { continue }
  $owned = $false
  foreach ($action in @($task.Actions)) {
    foreach ($executable in $ownedExecutables) {
      if (SamePath ([Environment]::ExpandEnvironmentVariables([string]$action.Execute)) $executable) { $owned = $true }
    }
  }
  if ($owned) {
    $ownedTaskNames += $taskName
    Stop-ScheduledTask -TaskName $taskName -ErrorAction SilentlyContinue
    Unregister-ScheduledTask -TaskName $taskName -Confirm:$false -ErrorAction SilentlyContinue
  }
}

$installDir = FullPath $plan.install_dir
$userPath = [Environment]::GetEnvironmentVariable('Path', 'User')
if ($userPath) {
  $entries = @($userPath -split ';' | Where-Object { $_ -and -not (SamePath $_ $installDir) })
  [Environment]::SetEnvironmentVariable('Path', ($entries -join ';'), 'User')
}

function Get-ContextBridgeCompletionBlock([string]$Text, [string]$OwnedCompletionPath) {
  if (-not $Text -or -not $OwnedCompletionPath) { return $null }
  $begin = [regex]::Matches($Text, '(?m)^# >>> ContextBridge completion >>>\r?$')
  $end = [regex]::Matches($Text, '(?m)^# <<< ContextBridge completion <<<\r?$')
  if ($begin.Count -ne 1 -or $end.Count -ne 1 -or $begin[0].Index -ge $end[0].Index) { return $null }
  $finish = $end[0].Index + $end[0].Length
  $block = $Text.Substring($begin[0].Index, $finish - $begin[0].Index)
  $expected = ". '" + (FullPath $OwnedCompletionPath).Replace("'", "''") + "'"
  $owned = @([regex]::Split($block, '\r?\n') | Where-Object { $_.Trim().Equals($expected, [StringComparison]::OrdinalIgnoreCase) })
  if ($owned.Count -ne 1) { return $null }
  return [pscustomobject]@{ Start = $begin[0].Index; Finish = $finish }
}
function Remove-ContextBridgeCompletionBlock([string]$ProfilePath, [string]$OwnedCompletionPath) {
  if (-not $ProfilePath -or -not (Test-Path -LiteralPath $ProfilePath -PathType Leaf)) { return }
  $text = [IO.File]::ReadAllText($ProfilePath)
  $block = Get-ContextBridgeCompletionBlock $text $OwnedCompletionPath
  if (-not $block) { return }
  $start = [int]$block.Start
  $finish = [int]$block.Finish
  if ($finish -lt $text.Length -and $text[$finish] -eq [char]10) { $finish++ }
  $updated = $text.Substring(0, $start) + $text.Substring($finish)
  [IO.File]::WriteAllText($ProfilePath, $updated, (New-Object Text.UTF8Encoding($false)))
}

$documents = [Environment]::GetFolderPath('MyDocuments')
$profileCandidates = @($PROFILE.CurrentUserAllHosts)
if ($documents) {
  $profileCandidates += (Join-Path $documents 'WindowsPowerShell\profile.ps1')
  $profileCandidates += (Join-Path $documents 'PowerShell\profile.ps1')
}
foreach ($profilePath in @($profileCandidates | Where-Object { $_ } | Sort-Object -Unique)) {
  Remove-ContextBridgeCompletionBlock $profilePath (Join-Path $installDir 'contextbridge-completion.ps1')
}

$programs = [Environment]::GetFolderPath('Programs')
if ($programs) {
  $menu = Join-Path $programs 'ContextBridge'
  if (Test-Path -LiteralPath $menu -PathType Container) {
    $shell = New-Object -ComObject WScript.Shell
    $entries = @(Get-ChildItem -LiteralPath $menu -Force)
    $owned = $entries.Count -gt 0
    foreach ($entry in $entries) {
      if ($entry.Extension -ne '.lnk') { $owned = $false; continue }
      $target = $shell.CreateShortcut($entry.FullName).TargetPath
      $targetOwned = $false
      foreach ($executable in $ownedExecutables) { if (SamePath $target $executable) { $targetOwned = $true } }
      if (-not $targetOwned) { $owned = $false }
    }
    if ($owned) { Remove-Item -LiteralPath $menu -Recurse -Force }
  }
}

foreach ($process in @(Get-Process contextbridge -ErrorAction SilentlyContinue)) {
  if ($process.Id -eq $ParentPID) { continue }
  $owned = $false
  foreach ($executable in $ownedExecutables) { if (SamePath $process.Path $executable) { $owned = $true } }
  if ($owned) { Stop-Process -Id $process.Id -Force -ErrorAction SilentlyContinue }
}

Wait-Process -Id $ParentPID -Timeout 60 -ErrorAction SilentlyContinue
Start-Sleep -Milliseconds 250

$targets = @($plan.program_paths) + @($plan.command_paths) + @($plan.purge_paths)
$targets = @($targets | Where-Object { $_ } | Sort-Object { ([string]$_).Length } -Descending -Unique)
foreach ($target in $targets) {
  Remove-Item -LiteralPath ([string]$target) -Recurse -Force -ErrorAction SilentlyContinue
}
foreach ($directory in @($plan.cleanup_dirs)) {
  if (-not $directory -or -not (Test-Path -LiteralPath $directory -PathType Container)) { continue }
  if (-not (Get-ChildItem -LiteralPath $directory -Force | Select-Object -First 1)) {
    Remove-Item -LiteralPath $directory -Force -ErrorAction SilentlyContinue
  }
}

$remaining = New-Object System.Collections.Generic.List[string]
foreach ($target in $targets) {
  if (Test-Path -LiteralPath ([string]$target)) { $remaining.Add('path: ' + [string]$target) }
}
$userPathAfter = [Environment]::GetEnvironmentVariable('Path', 'User')
foreach ($entry in @($userPathAfter -split ';' | Where-Object { $_ })) {
  if (SamePath $entry $installDir) { $remaining.Add('user PATH entry: ' + $entry) }
}
foreach ($taskName in $ownedTaskNames) {
  if (Get-ScheduledTask -TaskName $taskName -ErrorAction SilentlyContinue) { $remaining.Add('scheduled task: ' + $taskName) }
}
foreach ($profilePath in @($profileCandidates | Where-Object { $_ } | Sort-Object -Unique)) {
  if (-not (Test-Path -LiteralPath $profilePath -PathType Leaf)) { continue }
  $profileText = [IO.File]::ReadAllText($profilePath)
  if (Get-ContextBridgeCompletionBlock $profileText (Join-Path $installDir 'contextbridge-completion.ps1')) {
    $remaining.Add('PowerShell completion marker: ' + $profilePath)
  }
}
if ($programs) {
  $menu = Join-Path $programs 'ContextBridge'
  if (Test-Path -LiteralPath $menu -PathType Container) {
    $shell = New-Object -ComObject WScript.Shell
    foreach ($entry in @(Get-ChildItem -LiteralPath $menu -Filter '*.lnk' -File -ErrorAction SilentlyContinue)) {
      $target = $shell.CreateShortcut($entry.FullName).TargetPath
      foreach ($executable in $ownedExecutables) {
        if (SamePath $target $executable) { $remaining.Add('Start Menu shortcut: ' + $entry.FullName) }
      }
    }
  }
}
if ($remaining.Count -gt 0) {
  $lines = @('ContextBridge could not verify complete removal.', 'The following owned items remain:') + @($remaining | Sort-Object -Unique)
  [IO.File]::WriteAllLines([string]$plan.verification_log, $lines, (New-Object Text.UTF8Encoding($false)))
  exit 1
}

$helperDir = Split-Path -Parent $PlanPath
Remove-Item -LiteralPath $PlanPath -Force -ErrorAction SilentlyContinue
Remove-Item -LiteralPath $PSCommandPath -Force -ErrorAction SilentlyContinue
Start-Process -FilePath 'cmd.exe' -WindowStyle Hidden -ArgumentList @('/d','/c',('ping 127.0.0.1 -n 2 >nul & rmdir /s /q "' + $helperDir + '"'))
`
