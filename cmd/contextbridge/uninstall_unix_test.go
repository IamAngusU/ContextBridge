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
	content := "before\n# >>> ContextBridge completion >>>\nsource /managed/completion\n# <<< ContextBridge completion <<<\nafter\n"
	if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := removeManagedCompletionBlock(path); err != nil {
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
	if err := removeManagedCompletionBlock(path); err != nil {
		t.Fatal(err)
	}
	raw, err = os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if strings.TrimSpace(string(raw)) != strings.TrimSpace(malformed) {
		t.Fatal("ambiguous completion markers were rewritten")
	}
}
