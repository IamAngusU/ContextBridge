package main

import (
	"io/fs"
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
		".buildcheck":  true, // local ignored release verification output
		".git":         true,
		".github":      true,
		".tmp-metrics": true, // local ignored benchmark output
		"assets":       true,
		"cmd":          true,
		"deploy":       true,
		"docs":         true,
		"examples":     true,
		"internal":     true,
		"LICENSES":     true,
		"scripts":      true,
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
		"docs/schemas/verification-statement-v1.schema.json",
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

func TestPublicCoreDoesNotNamePrivateProviderAdapters(t *testing.T) {
	root := publicCoreRoot(t)
	textExtensions := map[string]bool{
		".css": true, ".go": true, ".html": true, ".js": true, ".json": true,
		".md": true, ".mod": true, ".ps1": true, ".sh": true, ".sum": true,
		".txt": true, ".yaml": true, ".yml": true,
	}
	// Construct these at runtime so the regression test does not itself place
	// private provider identifiers in the public source surface it scans.
	forbidden := []string{"chat" + "gpt", "gem" + "ini.google", "gem" + "ini", "chat." + "openai.com"}
	err := filepath.WalkDir(root, func(path string, entry fs.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		if entry.IsDir() {
			switch entry.Name() {
			case ".git", ".buildcheck", ".tmp-metrics", "vendor":
				if path != root {
					return filepath.SkipDir
				}
			}
			return nil
		}
		if !textExtensions[strings.ToLower(filepath.Ext(entry.Name()))] {
			return nil
		}
		raw, err := os.ReadFile(path)
		if err != nil {
			return err
		}
		lower := strings.ToLower(string(raw))
		for _, term := range forbidden {
			if strings.Contains(lower, term) {
				relative, _ := filepath.Rel(root, path)
				t.Errorf("public source contains private provider term %q in %s", term, filepath.ToSlash(relative))
			}
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
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
