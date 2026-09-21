package main

import (
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	"github.com/IamAngusU/ContextBridge/internal/config"
)

func TestUninstallPlanPreservesConfigurationWithoutPurge(t *testing.T) {
	installDir, configPath := makeUninstallFixture(t)
	plan, err := buildUninstallPlan(uninstallOptions{InstallDir: installDir, ConfigPath: configPath})
	if err != nil {
		t.Fatal(err)
	}
	if plan.Purge || len(plan.PurgePaths) != 0 {
		t.Fatalf("ordinary uninstall planned a purge: %#v", plan)
	}
	if !uninstallPathListContains(plan.ProgramPaths, fixtureUninstallBinary(installDir)) {
		t.Fatalf("program binary is absent from plan: %#v", plan.ProgramPaths)
	}
	if !uninstallPathListContains(plan.PreservedPaths, configPath) {
		t.Fatalf("config is not explicitly preserved: %#v", plan.PreservedPaths)
	}
}

func TestUninstallPurgeIncludesOnlyManagedPaths(t *testing.T) {
	installDir, configPath := makeUninstallFixture(t)
	plan, err := buildUninstallPlan(uninstallOptions{InstallDir: installDir, ConfigPath: configPath, Purge: true})
	if err != nil {
		t.Fatal(err)
	}
	for _, expected := range []string{
		configPath,
		filepath.Join(installDir, "data"),
		filepath.Join(installDir, "inbox"),
		filepath.Join(installDir, "models"),
		filepath.Join(installDir, "data", "rag"),
	} {
		if !uninstallPathListContains(plan.PurgePaths, expected) {
			t.Errorf("purge plan is missing managed path %s: %#v", expected, plan.PurgePaths)
		}
	}
}

func TestUninstallPurgePreservesExternalStorage(t *testing.T) {
	installDir, configPath := makeUninstallFixture(t)
	external := filepath.Join(t.TempDir(), "operator-artifacts")
	raw, err := os.ReadFile(configPath)
	if err != nil {
		t.Fatal(err)
	}
	externalYAML := filepath.ToSlash(external)
	raw = []byte(strings.Replace(string(raw), "  directory: ./data\n", "  directory: "+externalYAML+"\n", 1))
	if err := os.WriteFile(configPath, raw, 0o600); err != nil {
		t.Fatal(err)
	}
	plan, err := buildUninstallPlan(uninstallOptions{InstallDir: installDir, ConfigPath: configPath, Purge: true})
	if err != nil {
		t.Fatal(err)
	}
	if uninstallPathListContains(plan.PurgePaths, external) {
		t.Fatalf("external operator path entered purge plan: %#v", plan.PurgePaths)
	}
	found := false
	for _, preserved := range plan.PreservedPaths {
		if strings.Contains(preserved, external) {
			found = true
		}
	}
	if !found {
		t.Fatalf("external operator path is not reported as preserved: %#v", plan.PreservedPaths)
	}
}

func TestUninstallPurgeRejectsUnreadableConfigWithoutForce(t *testing.T) {
	installDir, configPath := makeUninstallFixture(t)
	if err := os.WriteFile(configPath, []byte("storage: [not valid"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := buildUninstallPlan(uninstallOptions{InstallDir: installDir, ConfigPath: configPath, Purge: true}); err == nil {
		t.Fatal("purge accepted an unreadable config without --force")
	}
	plan, err := buildUninstallPlan(uninstallOptions{InstallDir: installDir, ConfigPath: configPath, Purge: true, Force: true})
	if err != nil {
		t.Fatalf("forced bounded purge failed: %v", err)
	}
	if !uninstallPathListContains(plan.PurgePaths, filepath.Join(installDir, "data")) {
		t.Fatalf("forced purge did not retain the bounded default path: %#v", plan.PurgePaths)
	}
}

func TestUninstallInstallDirectoryMustBeInstallerOwnedAndNarrow(t *testing.T) {
	root := filepath.VolumeName(filepath.Clean(string(filepath.Separator))) + string(filepath.Separator)
	if validInstallDirectory(root) {
		t.Fatalf("filesystem root was accepted as an installation: %s", root)
	}
	if home, err := os.UserHomeDir(); err == nil && validInstallDirectory(home) {
		t.Fatalf("user home was accepted as an installation: %s", home)
	}
	directory := t.TempDir()
	if _, err := discoverInstallDirectory(directory, ""); err == nil {
		t.Fatal("unmarked directory was accepted as an installation")
	}
}

func TestSafeManagedDataPathRejectsLinkedEscape(t *testing.T) {
	root := t.TempDir()
	external := t.TempDir()
	linked := filepath.Join(root, "data")
	if err := os.Symlink(external, linked); err != nil {
		t.Skipf("symlinks unavailable: %v", err)
	}
	if safeManagedDataPath(filepath.Join(linked, "jobs"), root) {
		t.Fatal("managed path followed a symlink outside its owned root")
	}
}

func makeUninstallFixture(t *testing.T) (string, string) {
	t.Helper()
	installDir := t.TempDir()
	if err := os.WriteFile(fixtureUninstallBinary(installDir), []byte("fixture"), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(installDir, "config.example.yml"), []byte("# fixture\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	configPath := filepath.Join(installDir, "config.yml")
	if err := config.Default(configPath); err != nil {
		t.Fatal(err)
	}
	return installDir, configPath
}

func fixtureUninstallBinary(installDir string) string {
	name := "contextbridge"
	if runtime.GOOS == "windows" {
		name += ".exe"
	}
	return filepath.Join(installDir, name)
}

func uninstallPathListContains(paths []string, wanted string) bool {
	for _, path := range paths {
		if samePath(path, wanted) {
			return true
		}
	}
	return false
}
