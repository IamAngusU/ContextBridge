//go:build !windows

package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestManagedCompletionFileRequiresHeaderOwnership(t *testing.T) {
	if !managedCompletionFile([]byte("# ContextBridge managed completion\ncomplete ...\n")) {
		t.Fatal("bash completion ownership marker was not recognized")
	}
	if !managedCompletionFile([]byte("#compdef contextbridge cb\n# ContextBridge managed completion\n_arguments\n")) {
		t.Fatal("zsh completion ownership marker was not recognized")
	}
	if managedCompletionFile([]byte("# unrelated\necho '# ContextBridge managed completion'\n")) {
		t.Fatal("marker text outside the completion header was treated as ownership")
	}
}

func TestOwnedSystemdServiceRequiresDescriptionAndExactBinary(t *testing.T) {
	directory := t.TempDir()
	path := filepath.Join(directory, "contextbridge.service")
	binary := filepath.Join(directory, "contextbridge")
	content := "[Unit]\nDescription=ContextBridge local-first execution service\n[Service]\nExecStart=\"" + binary + "\" run --config /tmp/config.yml\n"
	if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
		t.Fatal(err)
	}
	if !ownedSystemdService(path, binary, "Description=ContextBridge local-first execution service") {
		t.Fatal("installer-shaped unit was not recognized")
	}
	if ownedSystemdService(path, binary+"-other", "Description=ContextBridge local-first execution service") {
		t.Fatal("unit with another executable was treated as owned")
	}
}

func TestRemoveManagedCompletionBlockPreservesUserProfileText(t *testing.T) {
	path := filepath.Join(t.TempDir(), ".zshrc")
	content := "before\n# >>> ContextBridge completion >>>\nfpath=(\"$HOME/.zfunc\" $fpath)\nif (( $+functions[compdef] )); then compdef _contextbridge cb; fi\n# <<< ContextBridge completion <<<\nafter\n"
	if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := removeManagedCompletionBlock(path, []string{"cb"}); err != nil {
		t.Fatal(err)
	}
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if string(raw) != "before\nafter\n" {
		t.Fatalf("user profile text changed unexpectedly: %q", raw)
	}

	malformed := "keep\n# >>> ContextBridge completion >>>\n# >>> ContextBridge completion >>>\n# <<< ContextBridge completion <<<\n"
	if err := os.WriteFile(path, []byte(malformed), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := removeManagedCompletionBlock(path, []string{"cb"}); err != nil {
		t.Fatal(err)
	}
	raw, err = os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if strings.TrimSpace(string(raw)) != strings.TrimSpace(malformed) {
		t.Fatal("ambiguous completion markers were rewritten")
	}

	foreign := "keep\n# >>> ContextBridge completion >>>\nif (( $+functions[compdef] )); then compdef _contextbridge other-cb; fi\n# <<< ContextBridge completion <<<\n"
	if err := os.WriteFile(path, []byte(foreign), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := removeManagedCompletionBlock(path, []string{"cb"}); err != nil {
		t.Fatal(err)
	}
	raw, err = os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if string(raw) != foreign {
		t.Fatal("another installation's completion block was removed")
	}
}

func TestOwnedLaunchAgentParsesEscapedAuthoritativeValues(t *testing.T) {
	raw := []byte(`<?xml version="1.0" encoding="UTF-8"?>
<plist version="1.0"><dict>
  <key>Label</key><string>de.angusu.contextbridge</string>
  <key>ProgramArguments</key><array><string>/Users/A &amp; B/ContextBridge/contextbridge</string><string>run</string></array>
</dict></plist>`)
	if !ownedLaunchAgent(raw, "/Users/A & B/ContextBridge/contextbridge", "de.angusu.contextbridge") {
		t.Fatal("escaped installer path was not recognized as an owned LaunchAgent")
	}
	if ownedLaunchAgent(raw, "/Users/other/contextbridge", "de.angusu.contextbridge") {
		t.Fatal("foreign binary path was accepted as an owned LaunchAgent")
	}
	spoofed := []byte(`<?xml version="1.0"?><plist><dict>
<key>Label</key><string>foreign.agent</string>
<key>Comment</key><string>de.angusu.contextbridge /Users/A &amp; B/ContextBridge/contextbridge</string>
<key>ProgramArguments</key><array><string>/usr/bin/true</string></array>
</dict></plist>`)
	if ownedLaunchAgent(spoofed, "/Users/A & B/ContextBridge/contextbridge", "de.angusu.contextbridge") {
		t.Fatal("matching text in an unrelated plist key bypassed ownership checks")
	}
}

func TestDiscoverManagedCommandsFindsEveryCanonicalAlias(t *testing.T) {
	installDir := t.TempDir()
	binDir := t.TempDir()
	installBinary := filepath.Join(installDir, "contextbridge")
	if err := os.WriteFile(installBinary, []byte("fixture"), 0o700); err != nil {
		t.Fatal(err)
	}
	for _, name := range []string{"contextbridge", "cb", "bridge-ai"} {
		if err := os.Symlink(installBinary, filepath.Join(binDir, name)); err != nil {
			t.Skipf("symlinks unavailable: %v", err)
		}
	}
	t.Setenv("CONTEXTBRIDGE_BIN_DIR", binDir)
	paths := discoverManagedCommandPaths(installBinary, installBinary)
	for _, name := range []string{"contextbridge", "cb", "bridge-ai"} {
		if !uninstallPathListContains(paths, filepath.Join(binDir, name)) {
			t.Errorf("owned command alias %s was not discovered: %#v", name, paths)
		}
	}
}
