//go:build !windows

package main

import (
	"bytes"
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"time"
)

func addPlatformUninstallPlan(plan *uninstallPlan) error {
	return nil
}

func executeUninstall(plan uninstallPlan) error {
	removeUnixAutostart(plan)
	removeUnixCompletions()
	for _, path := range uniqueCleanPaths(append(append(append([]string{}, plan.ProgramPaths...), plan.CommandPaths...), plan.PurgePaths...)) {
		if err := os.RemoveAll(path); err != nil {
			return fmt.Errorf("remove %s: %w", path, err)
		}
	}
	for _, directory := range plan.CleanupDirs {
		if empty, err := directoryEmpty(directory); err == nil && empty {
			_ = os.Remove(directory)
		}
	}
	fmt.Println("ContextBridge program files and owned integrations were removed.")
	if !plan.Purge {
		fmt.Printf("Configuration and managed data were preserved at %s.\n", plan.ConfigPath)
	}
	return nil
}

func removeUnixAutostart(plan uninstallPlan) {
	home, err := os.UserHomeDir()
	if err != nil {
		return
	}
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	if runtime.GOOS == "linux" {
		unitDir := filepath.Join(home, ".config", "systemd", "user")
		servicePath := filepath.Join(unitDir, "contextbridge.service")
		updatePath := filepath.Join(unitDir, "contextbridge-update.service")
		timerPath := filepath.Join(unitDir, "contextbridge-update.timer")
		serviceOwned := ownedSystemdService(servicePath, plan.InstallBinary, "Description=ContextBridge local-first execution service")
		updateOwned := ownedSystemdService(updatePath, plan.InstallBinary, "Description=ContextBridge verified automatic update")
		timerOwned := updateOwned && boundedFileContains(timerPath, "Description=Check for ContextBridge updates daily", "OnUnitActiveSec=24h")
		if serviceOwned || updateOwned {
			arguments := []string{"--user", "disable", "--now"}
			if serviceOwned {
				arguments = append(arguments, "contextbridge.service")
			}
			if timerOwned {
				arguments = append(arguments, "contextbridge-update.timer")
			}
			_ = exec.CommandContext(ctx, "systemctl", arguments...).Run()
			if serviceOwned {
				_ = os.Remove(servicePath)
			}
			if updateOwned {
				_ = os.Remove(updatePath)
			}
			if timerOwned {
				_ = os.Remove(timerPath)
			}
			_ = exec.CommandContext(ctx, "systemctl", "--user", "daemon-reload").Run()
		}
		return
	}
	if runtime.GOOS == "darwin" {
		agentDir := filepath.Join(home, "Library", "LaunchAgents")
		for _, spec := range []struct{ label, file string }{
			{"de.angusu.contextbridge", "de.angusu.contextbridge.plist"},
			{"de.angusu.contextbridge.update", "de.angusu.contextbridge.update.plist"},
		} {
			path := filepath.Join(agentDir, spec.file)
			raw, err := readSmallRegularFile(path, 256<<10)
			if err != nil || !bytes.Contains(raw, []byte(plan.InstallBinary)) || !bytes.Contains(raw, []byte(spec.label)) {
				continue
			}
			_ = exec.CommandContext(ctx, "launchctl", "bootout", fmt.Sprintf("gui/%d", os.Getuid()), path).Run()
			_ = os.Remove(path)
		}
	}
}

func removeUnixCompletions() {
	home, err := os.UserHomeDir()
	if err != nil {
		return
	}
	dataHome := strings.TrimSpace(os.Getenv("XDG_DATA_HOME"))
	if dataHome == "" {
		dataHome = filepath.Join(home, ".local", "share")
	}
	for _, directory := range []string{
		filepath.Join(dataHome, "bash-completion", "completions"),
		filepath.Join(home, ".zfunc"),
	} {
		entries, err := os.ReadDir(directory)
		if err != nil {
			continue
		}
		for _, entry := range entries {
			if entry.IsDir() {
				continue
			}
			path := filepath.Join(directory, entry.Name())
			raw, err := readSmallRegularFile(path, 256<<10)
			if err == nil && managedCompletionFile(raw) {
				_ = os.Remove(path)
			}
		}
	}
	zshrc := filepath.Join(home, ".zshrc")
	if zdot := strings.TrimSpace(os.Getenv("ZDOTDIR")); zdot != "" {
		zshrc = filepath.Join(zdot, ".zshrc")
	}
	_ = removeManagedCompletionBlock(zshrc)
}

func ownedSystemdService(path, installBinary, description string) bool {
	raw, err := readSmallRegularFile(path, 128<<10)
	if err != nil {
		return false
	}
	return bytes.Contains(raw, []byte(description)) && bytes.Contains(raw, []byte(`ExecStart="`+installBinary+`"`))
}

func boundedFileContains(path string, fragments ...string) bool {
	raw, err := readSmallRegularFile(path, 128<<10)
	if err != nil {
		return false
	}
	for _, fragment := range fragments {
		if !bytes.Contains(raw, []byte(fragment)) {
			return false
		}
	}
	return true
}

func managedCompletionFile(raw []byte) bool {
	return bytes.HasPrefix(raw, []byte("# ContextBridge managed completion\n")) ||
		(bytes.HasPrefix(raw, []byte("#compdef contextbridge cb\n")) && bytes.Contains(raw[:min(len(raw), 128)], []byte("# ContextBridge managed completion\n")))
}

func removeManagedCompletionBlock(path string) error {
	resolved := path
	if target, err := filepath.EvalSymlinks(path); err == nil {
		resolved = target
	}
	raw, err := readSmallRegularFile(resolved, 2<<20)
	if err != nil {
		return nil
	}
	beginMarker := []byte("# >>> ContextBridge completion >>>")
	endMarker := []byte("# <<< ContextBridge completion <<<")
	if bytes.Count(raw, beginMarker) != 1 || bytes.Count(raw, endMarker) != 1 {
		return nil
	}
	start := bytes.Index(raw, beginMarker)
	endAt := bytes.Index(raw, endMarker)
	if start < 0 || endAt <= start {
		return nil
	}
	end := endAt + len(endMarker)
	if end < len(raw) && raw[end] == '\r' {
		end++
	}
	if end < len(raw) && raw[end] == '\n' {
		end++
	}
	updated := append(append([]byte{}, raw[:start]...), raw[end:]...)
	info, err := os.Stat(resolved)
	if err != nil {
		return err
	}
	temporary, err := os.CreateTemp(filepath.Dir(resolved), ".contextbridge-uninstall-*")
	if err != nil {
		return err
	}
	temporaryPath := temporary.Name()
	defer os.Remove(temporaryPath)
	if err := temporary.Chmod(info.Mode().Perm()); err != nil {
		temporary.Close()
		return err
	}
	if _, err := temporary.Write(updated); err != nil {
		temporary.Close()
		return err
	}
	if err := temporary.Sync(); err != nil {
		temporary.Close()
		return err
	}
	if err := temporary.Close(); err != nil {
		return err
	}
	return os.Rename(temporaryPath, resolved)
}

func directoryEmpty(path string) (bool, error) {
	entries, err := os.ReadDir(path)
	if os.IsNotExist(err) {
		return false, nil
	}
	return err == nil && len(entries) == 0, err
}
