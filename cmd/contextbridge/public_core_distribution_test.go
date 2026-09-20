package main

import (
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

// This repository is the provider-neutral public core. Keep implementation-
// specific adapters out-of-tree so a private adapter cannot accidentally enter
// a public release through a later merge or release-script change.
func TestPublicCoreContainsNoBundledVendorAdapter(t *testing.T) {
	_, source, _, ok := runtime.Caller(0)
	if !ok {
		t.Fatal("cannot locate public-core source tree")
	}
	root := filepath.Clean(filepath.Join(filepath.Dir(source), "..", ".."))
	for _, relative := range []string{"extension", filepath.Join("cmd", "contextbridge", "browser_inspect.go")} {
		if _, err := os.Stat(filepath.Join(root, relative)); err == nil || !os.IsNotExist(err) {
			t.Fatalf("public core contains private adapter path %q", relative)
		}
	}

	banned := []string{
		"chat" + "gpt",
		"gem" + "ini",
		"chro" + "mium",
		"fire" + "fox",
		"play" + "wright",
		"sele" + "nium",
		"prompt-" + "textarea",
		"conversation-" + "turn",
		"model-" + "response",
	}
	textExtensions := map[string]bool{
		".css": true, ".go": true, ".html": true, ".js": true, ".json": true,
		".md": true, ".php": true, ".ps1": true, ".sh": true, ".txt": true,
		".yaml": true, ".yml": true,
	}
	err := filepath.WalkDir(root, func(path string, entry os.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		if entry.IsDir() {
			if entry.Name() == ".git" || entry.Name() == ".buildcheck" {
				return filepath.SkipDir
			}
			return nil
		}
		if !textExtensions[strings.ToLower(filepath.Ext(path))] {
			return nil
		}
		raw, readErr := os.ReadFile(path)
		if readErr != nil {
			return readErr
		}
		lower := strings.ToLower(string(raw))
		for _, token := range banned {
			if strings.Contains(lower, token) {
				relative, _ := filepath.Rel(root, path)
				t.Fatalf("public core contains a private adapter fingerprint in %s", relative)
			}
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
}
