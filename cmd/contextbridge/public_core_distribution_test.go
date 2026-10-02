package main

import (
	"os"
	"path/filepath"
	"runtime"
	"strings"
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
		".buildcheck":       true, // local ignored release verification output
		".git":              true,
		".github":           true,
		".tmp-metrics":      true, // local ignored benchmark output
		".tmp":              true, // local ignored CI/build scratch output
		"assets":            true,
		"cmd":               true,
		"deploy":            true,
		"docs":              true,
		"examples":          true,
		"internal":          true,
		"LICENSES":          true,
		"release-artifacts": true, // local ignored output created by build-release.ps1
		"scripts":           true,
	}
	allowedFiles := map[string]bool{
		".editorconfig":            true,
		".gitattributes":           true,
		".git":                     true, // file in linked Git worktrees; directory in ordinary clones
		".gitleaks.toml":           true,
		".gitignore":               true,
		".markdownlint-cli2.jsonc": true,
		"AGENTS.md":                true,
		"CHANGELOG.md":             true,
		"config.example.yml":       true,
		"CONTRIBUTING.md":          true,
		"go.mod":                   true,
		"go.sum":                   true,
		"install.ps1":              true,
		"install.sh":               true,
		"LICENSE":                  true,
		"LICENSING.md":             true,
		"Makefile":                 true,
		"NOTICE":                   true,
		"README.md":                true,
		"SECURITY.md":              true,
		"TRADEMARKS.md":            true,
		"THIRD_PARTY_NOTICES.txt":  true,
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

func TestPublicCoreProductSurfaceIsPresent(t *testing.T) {
	root := publicCoreRoot(t)
	required := []string{
		"assets/brand/contextbridge-wordmark.svg",
		"internal/bridge/dashboard/mark.svg",
		"docs/README.md",
		"docs/compatibility.md",
		"docs/verification.md",
		"docs/schemas/adapter-conformance-v1.schema.json",
		"docs/schemas/verification-statement-v1.schema.json",
		"docs/schemas/adapter-presence-v1.schema.json",
		"docs/schemas/verification-trust-key-v1.schema.json",
		"docs/operations.md",
		"docs/limits-and-performance.md",
		"docs/bounded-agent.md",
		"docs/integrations.md",
		"docs/automation.md",
		"docs/pools-and-placement.md",
		"docs/portable-resources.md",
	}
	for _, name := range required {
		info, err := os.Stat(filepath.Join(root, filepath.FromSlash(name)))
		if err != nil || !info.Mode().IsRegular() || info.Size() == 0 {
			t.Errorf("required public product surface %q is missing or empty", name)
		}
	}
	readme, err := os.ReadFile(filepath.Join(root, "README.md"))
	if err != nil {
		t.Fatal(err)
	}
	for _, reference := range []string{
		"assets/brand/contextbridge-wordmark.svg",
		"docs/README.md",
		"docs/compatibility.md",
		"docs/verification.md",
		"docs/limits-and-performance.md",
	} {
		if !strings.Contains(string(readme), reference) {
			t.Errorf("README does not expose required public surface %q", reference)
		}
	}
}

func publicCoreRoot(t *testing.T) string {
	t.Helper()
	_, source, _, ok := runtime.Caller(0)
	if !ok {
		t.Fatal("cannot locate public-core source tree")
	}
	return filepath.Clean(filepath.Join(filepath.Dir(source), "..", ".."))
}
