package main

import (
	"encoding/json"
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

func TestUninstallPlanIncludesExactUpdaterOwnedArtifacts(t *testing.T) {
	installDir, configPath := makeUninstallFixture(t)
	binary := fixtureUninstallBinary(installDir)
	wanted := []string{binary + ".previous", binary + ".next", binary + ".failed", binary + ".update-pending.json", binary + ".update-pending.json.tmp"}
	foreignPlatformArtifact := binary + ".next.exe"
	if runtime.GOOS == "windows" {
		wanted = []string{binary + ".previous.exe", binary + ".next.exe", binary + ".failed.exe", binary + ".update.ps1"}
		foreignPlatformArtifact = binary + ".update-pending.json"
	}
	for _, path := range wanted {
		if err := os.WriteFile(path, []byte("owned update artifact"), 0600); err != nil {
			t.Fatal(err)
		}
	}
	unrelated := binary + ".previous.notes"
	if err := os.WriteFile(unrelated, []byte("operator file"), 0600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(foreignPlatformArtifact, []byte("not owned on this platform"), 0600); err != nil {
		t.Fatal(err)
	}
	plan, err := buildUninstallPlan(uninstallOptions{InstallDir: installDir, ConfigPath: configPath, Purge: true})
	if err != nil {
		t.Fatal(err)
	}
	for _, path := range wanted {
		if !uninstallPathListContains(plan.ProgramPaths, path) {
			t.Errorf("updater artifact missing from uninstall plan: %s", path)
		}
	}
	if uninstallPathListContains(plan.ProgramPaths, unrelated) {
		t.Fatal("similar unrelated sibling entered uninstall plan")
	}
	if uninstallPathListContains(plan.ProgramPaths, foreignPlatformArtifact) {
		t.Fatal("another platform's updater suffix entered uninstall plan")
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

func TestSafeManagedDataPathRejectsDangerousManagedRoots(t *testing.T) {
	installDir := t.TempDir()
	volumeRoot := filepath.VolumeName(installDir) + string(filepath.Separator)
	if safeManagedDataPath(filepath.Join(volumeRoot, "data"), volumeRoot) {
		t.Fatalf("filesystem root was accepted as managed purge authority: %s", volumeRoot)
	}
	home, err := os.UserHomeDir()
	if err == nil && safeManagedDataPath(filepath.Join(home, "data"), home) {
		t.Fatalf("user home was accepted as managed purge authority: %s", home)
	}
}

func TestInstallOwnershipManifestAddsPrivateAgnosticProgramPaths(t *testing.T) {
	installDir, configPath := makeUninstallFixture(t)
	additional := filepath.Join(installDir, "optional-adapter")
	if err := os.Mkdir(additional, 0o700); err != nil {
		t.Fatal(err)
	}
	writeInstallOwnershipManifest(t, installDir, []string{
		filepath.ToSlash(filepath.Base(fixtureUninstallBinary(installDir))),
		"config.example.yml",
		"optional-adapter",
	})
	plan, err := buildUninstallPlan(uninstallOptions{InstallDir: installDir, ConfigPath: configPath})
	if err != nil {
		t.Fatal(err)
	}
	for _, expected := range []string{additional, filepath.Join(installDir, installOwnershipManifestName)} {
		if !uninstallPathListContains(plan.ProgramPaths, expected) {
			t.Errorf("manifest-owned path is absent from uninstall plan: %s", expected)
		}
	}
}

func TestInstallOwnershipManifestRejectsUnsafeOrMutablePaths(t *testing.T) {
	for name, unsafePath := range map[string]string{
		"escape":  "../outside",
		"mutable": "data/jobs.db",
		"rooted":  "/outside",
	} {
		t.Run(name, func(t *testing.T) {
			installDir, configPath := makeUninstallFixture(t)
			writeInstallOwnershipManifest(t, installDir, []string{
				filepath.ToSlash(filepath.Base(fixtureUninstallBinary(installDir))),
				"config.example.yml",
				unsafePath,
			})
			if _, err := buildUninstallPlan(uninstallOptions{InstallDir: installDir, ConfigPath: configPath, Force: true}); err == nil {
				t.Fatalf("unsafe manifest path %q was accepted", unsafePath)
			}
		})
	}
}

func TestInstallOwnershipManifestRequiresBinaryAndMarker(t *testing.T) {
	installDir, configPath := makeUninstallFixture(t)
	writeInstallOwnershipManifest(t, installDir, []string{"README.md"})
	if _, err := buildUninstallPlan(uninstallOptions{InstallDir: installDir, ConfigPath: configPath}); err == nil {
		t.Fatal("manifest without binary and installer marker was accepted")
	}
}

func writeInstallOwnershipManifest(t *testing.T, installDir string, paths []string) {
	t.Helper()
	raw, err := json.Marshal(installOwnershipManifest{SchemaVersion: 1, Product: "ContextBridge", Paths: paths})
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(installDir, installOwnershipManifestName), raw, 0o600); err != nil {
		t.Fatal(err)
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
