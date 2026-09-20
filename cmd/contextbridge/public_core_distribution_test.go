package main

import (
	"os"
	"path/filepath"
	"runtime"
	"testing"
)

// The public repository is an intentionally small, provider-neutral source
// distribution. Treat its root surface as an allowlist so an optional
// out-of-tree component cannot enter a release through a later merge or build
// change without an explicit public-surface review.
func TestPublicCoreDistributionSurfaceIsExplicit(t *testing.T) {
	_, source, _, ok := runtime.Caller(0)
	if !ok {
		t.Fatal("cannot locate public-core source tree")
	}
	root := filepath.Clean(filepath.Join(filepath.Dir(source), "..", ".."))
	allowedDirectories := map[string]bool{
		".buildcheck": true, // local ignored release verification output
		".git":        true,
		".github":     true,
		"assets":      true,
		"cmd":         true,
		"deploy":      true,
		"docs":        true,
		"examples":    true,
		"internal":    true,
		"scripts":     true,
	}
	allowedFiles := map[string]bool{
		".editorconfig":            true,
		".gitattributes":           true,
		".gitignore":               true,
		".markdownlint-cli2.jsonc": true,
		"CHANGELOG.md":             true,
		"config.example.yml":       true,
		"CONTRIBUTING.md":          true,
		"go.mod":                   true,
		"go.sum":                   true,
		"install.ps1":              true,
		"install.sh":               true,
		"LICENSE":                  true,
		"Makefile":                 true,
		"README.md":                true,
		"SECURITY.md":              true,
	}
	entries, err := os.ReadDir(root)
	if err != nil {
		t.Fatal(err)
	}
	for _, entry := range entries {
		if entry.IsDir() {
			if !allowedDirectories[entry.Name()] {
				t.Fatalf("public source distribution contains an unreviewed root directory %q", entry.Name())
			}
			continue
		}
		if !allowedFiles[entry.Name()] {
			t.Fatalf("public source distribution contains an unreviewed root file %q", entry.Name())
		}
	}
}
