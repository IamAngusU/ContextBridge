//go:build windows

package updater

import (
	"context"
	"os"
	"os/exec"
	"strings"
)

func automaticInstallReady(ctx context.Context, executable string) bool {
	if len(os.Args) > 1 && (os.Args[1] == "run" || os.Args[1] == "serve") {
		return false
	}
	// A portable or manually opened Windows console has no process manager
	// that can close and restart it safely. The scheduled updater defers in
	// that case instead of repeatedly failing and quarantining a good release.
	command := exec.CommandContext(ctx, "powershell.exe", "-NoLogo", "-NoProfile", "-NonInteractive", "-Command",
		`$task = Get-ScheduledTask -TaskName 'ContextBridge' -ErrorAction SilentlyContinue; if ($task -and $task.State -eq 'Running' -and $task.Actions.Count -gt 0 -and $task.Actions[0].Execute -eq $env:CONTEXTBRIDGE_UPDATE_TARGET_EXE) { [Console]::Write('ready') }`)
	command.Env = append(os.Environ(), "CONTEXTBRIDGE_UPDATE_TARGET_EXE="+executable)
	output, err := command.Output()
	return err == nil && strings.TrimSpace(string(output)) == "ready"
}
