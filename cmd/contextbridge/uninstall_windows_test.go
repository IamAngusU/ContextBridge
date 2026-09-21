//go:build windows

package main

import (
	"os/exec"
	"strings"
	"testing"
)

func TestWindowsUninstallHelperParsesAndKeepsOwnershipChecks(t *testing.T) {
	powerShell, err := exec.LookPath("powershell.exe")
	if err != nil {
		t.Skip("Windows PowerShell is unavailable")
	}
	command := exec.Command(powerShell, "-NoLogo", "-NoProfile", "-NonInteractive", "-Command", "$null = [ScriptBlock]::Create([Console]::In.ReadToEnd())")
	command.Stdin = strings.NewReader(windowsUninstallHelper)
	if output, err := command.CombinedOutput(); err != nil {
		t.Fatalf("PowerShell could not parse uninstall helper: %v\n%s", err, output)
	}
	for _, evidence := range []string{
		"SamePath ([Environment]::ExpandEnvironmentVariables([string]$action.Execute)) $executable",
		"function Get-ContextBridgeCompletionBlock",
		"$owned.Count -ne 1",
		"Join-Path $installDir 'contextbridge-completion.ps1'",
		"if (-not $targetOwned) { $owned = $false }",
		"Remove-Item -LiteralPath ([string]$target)",
		"ContextBridge could not verify complete removal.",
		"[IO.File]::WriteAllLines([string]$plan.verification_log",
	} {
		if !strings.Contains(windowsUninstallHelper, evidence) {
			t.Errorf("helper is missing ownership or literal-path evidence %q", evidence)
		}
	}
}
