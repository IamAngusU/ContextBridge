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

func replaceExecutable(current, staged, expectedVersion string) (bool, error) {
	next := current + ".next.exe"
	backup := current + ".previous.exe"
	script := current + ".update.ps1"
	_ = os.Remove(next)
	if err := copyFile(staged, next, 0700); err != nil {
		return false, err
	}
	if err := validateExecutable(next, expectedVersion); err != nil {
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
try { Wait-Process -Id $parentPid -Timeout 60 -ErrorAction SilentlyContinue } catch {}
$managedTask = Get-ScheduledTask -TaskName "ContextBridge" -ErrorAction SilentlyContinue
if ($managedTask) {
  Stop-ScheduledTask -TaskName "ContextBridge" -ErrorAction SilentlyContinue
  Start-Sleep -Milliseconds 500
}
for ($attempt = 0; $attempt -lt 30; $attempt++) {
  try {
    if (Test-Path $backup) { Remove-Item $backup -Force }
    Move-Item $current $backup -Force
    Move-Item $next $current -Force
    $reported = (& $current version | Out-String).Trim()
    if ($LASTEXITCODE -ne 0 -or $reported -ne $expected) { throw "Updated executable validation failed." }
    if ($managedTask) {
      Start-ScheduledTask -TaskName "ContextBridge" -ErrorAction SilentlyContinue
    } elseif ($restartLine) {
      Start-Process -FilePath $current -ArgumentList $restartLine -WindowStyle Hidden
    }
    exit 0
  } catch {
    if ((Test-Path $backup) -and -not (Test-Path $current)) { Move-Item $backup $current -Force -ErrorAction SilentlyContinue }
    Start-Sleep -Seconds 1
  }
}
if ((Test-Path $backup) -and -not (Test-Path $current)) { Move-Item $backup $current -Force -ErrorAction SilentlyContinue }
if ($managedTask) { Start-ScheduledTask -TaskName "ContextBridge" -ErrorAction SilentlyContinue }
elseif ($restartLine -and (Test-Path $current)) { Start-Process -FilePath $current -ArgumentList $restartLine -WindowStyle Hidden }
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
	command := exec.Command("powershell.exe", "-NoLogo", "-NoProfile", "-NonInteractive", "-ExecutionPolicy", "Bypass", "-File", script, current, next, backup, expectedVersion, strconv.Itoa(os.Getpid()), encodedRestart)
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
