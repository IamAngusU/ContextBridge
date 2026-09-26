//go:build !windows

package main

import (
	"bytes"
	"context"
	"encoding/xml"
	"fmt"
	"io"
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
	removeUnixCompletions(plan)
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
			if err != nil || !ownedLaunchAgent(raw, plan.InstallBinary, spec.label) {
				continue
			}
			_ = exec.CommandContext(ctx, "launchctl", "bootout", fmt.Sprintf("gui/%d", os.Getuid()), path).Run()
			_ = os.Remove(path)
		}
	}
}

// ownedLaunchAgent parses the two authoritative plist values instead of
// searching raw XML bytes. Dynamic installer paths are XML-escaped, and a raw
// substring check both misses legitimate paths containing '&' and can accept a
// matching string in an unrelated key or comment.
func ownedLaunchAgent(raw []byte, installBinary, label string) bool {
	decoder := xml.NewDecoder(bytes.NewReader(raw))
	pendingKey := ""
	programArgumentsDepth := 0
	programBinary := ""
	parsedLabel := ""
	programArgumentsSeen := false
	labelSeen := false
	for {
		token, err := decoder.Token()
		if err != nil {
			return err == io.EOF && parsedLabel == label && programBinary == installBinary
		}
		switch value := token.(type) {
		case xml.StartElement:
			switch value.Name.Local {
			case "key":
				var key string
				if err := decoder.DecodeElement(&key, &value); err != nil {
					return false
				}
				pendingKey = strings.TrimSpace(key)
			case "array":
				if pendingKey == "ProgramArguments" {
					if programArgumentsSeen {
						return false
					}
					programArgumentsSeen = true
					programArgumentsDepth = 1
				} else if programArgumentsDepth > 0 {
					programArgumentsDepth++
				}
				pendingKey = ""
			case "string":
				var stringValue string
				if err := decoder.DecodeElement(&stringValue, &value); err != nil {
					return false
				}
				if programArgumentsDepth > 0 && programBinary == "" {
					programBinary = stringValue
				} else if pendingKey == "Label" {
					if labelSeen {
						return false
					}
					labelSeen = true
					parsedLabel = stringValue
				}
				pendingKey = ""
			}
		case xml.EndElement:
			if value.Name.Local == "array" && programArgumentsDepth > 0 {
				programArgumentsDepth--
			}
		}
	}
}

func removeUnixCompletions(plan uninstallPlan) {
	home, err := os.UserHomeDir()
	if err != nil {
		return
	}
	commandNames := make([]string, 0, len(plan.CommandPaths))
	seenNames := map[string]bool{}
	for _, commandPath := range plan.CommandPaths {
		name := filepath.Base(commandPath)
		if name == "." || name == string(filepath.Separator) || seenNames[name] {
			continue
		}
		seenNames[name] = true
		commandNames = append(commandNames, name)
	}
	if len(commandNames) == 0 {
		return
	}
	dataHome := strings.TrimSpace(os.Getenv("XDG_DATA_HOME"))
	if dataHome == "" {
		dataHome = filepath.Join(home, ".local", "share")
	}
	for _, name := range commandNames {
		for _, path := range []string{
			filepath.Join(dataHome, "bash-completion", "completions", name),
			filepath.Join(home, ".zfunc", "_"+name),
		} {
			raw, err := readSmallRegularFile(path, 256<<10)
			if err == nil && managedCompletionFile(raw) {
				// #nosec G703 -- path is a fixed per-user completion location and
				// readSmallRegularFile rejects symlinks and non-regular files first.
				_ = os.Remove(path)
			}
		}
	}
	zshrc := filepath.Join(home, ".zshrc")
	if zdot := strings.TrimSpace(os.Getenv("ZDOTDIR")); zdot != "" {
		zshrc = filepath.Join(zdot, ".zshrc")
	}
	_ = removeManagedCompletionBlock(zshrc, commandNames)
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

func removeManagedCompletionBlock(path string, ownedCommandNames []string) error {
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
	block := string(raw[start : endAt+len(endMarker)])
	owned := false
	for _, name := range ownedCommandNames {
		for _, line := range strings.Split(block, "\n") {
			marker := "compdef _contextbridge "
			at := strings.Index(line, marker)
			if at < 0 {
				continue
			}
			for _, candidate := range strings.FieldsFunc(line[at+len(marker):], func(r rune) bool {
				return r == ' ' || r == '\t' || r == ';' || r == '\r'
			}) {
				if candidate == name {
					owned = true
				}
			}
		}
	}
	if !owned {
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
	// #nosec G703 -- resolved is the evaluated target of the fixed per-user
	// shell profile path; readSmallRegularFile already rejected symlinks,
	// non-regular files, and files beyond the bounded size.
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
	// #nosec G703 -- both operands are confined to the already validated
	// profile directory; the temporary file was created there by os.CreateTemp.
	return os.Rename(temporaryPath, resolved)
}

func directoryEmpty(path string) (bool, error) {
	entries, err := os.ReadDir(path)
	if os.IsNotExist(err) {
		return false, nil
	}
	return err == nil && len(entries) == 0, err
}
