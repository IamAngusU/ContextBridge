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

function Remove-ContextBridgeCompletionBlock([string]$ProfilePath) {
  if (-not $ProfilePath -or -not (Test-Path -LiteralPath $ProfilePath -PathType Leaf)) { return }
  $text = [IO.File]::ReadAllText($ProfilePath)
  $begin = [regex]::Matches($text, '(?m)^# >>> ContextBridge completion >>>\r?$')
  $end = [regex]::Matches($text, '(?m)^# <<< ContextBridge completion <<<\r?$')
  if ($begin.Count -ne 1 -or $end.Count -ne 1 -or $begin[0].Index -ge $end[0].Index) { return }
  $start = $begin[0].Index
  $finish = $end[0].Index + $end[0].Length
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
  Remove-ContextBridgeCompletionBlock $profilePath
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

$helperDir = Split-Path -Parent $PlanPath
Remove-Item -LiteralPath $PlanPath -Force -ErrorAction SilentlyContinue
Remove-Item -LiteralPath $PSCommandPath -Force -ErrorAction SilentlyContinue
Start-Process -FilePath 'cmd.exe' -WindowStyle Hidden -ArgumentList @('/d','/c',('ping 127.0.0.1 -n 2 >nul & rmdir /s /q "' + $helperDir + '"'))
`
