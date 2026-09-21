package main

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"net"
	"net/http"
	"os"
	pathpkg "path"
	"path/filepath"
	"runtime"
	"sort"
	"strings"
	"time"

	"github.com/IamAngusU/ContextBridge/internal/config"
)

type uninstallPlan struct {
	InstallDir      string   `json:"install_dir"`
	InstallBinary   string   `json:"install_binary"`
	CurrentBinary   string   `json:"current_binary"`
	ConfigPath      string   `json:"config_path"`
	ProgramPaths    []string `json:"program_paths"`
	CommandPaths    []string `json:"command_paths"`
	PurgePaths      []string `json:"purge_paths,omitempty"`
	PreservedPaths  []string `json:"preserved_paths,omitempty"`
	CleanupDirs     []string `json:"cleanup_dirs,omitempty"`
	VerificationLog string   `json:"verification_log,omitempty"`
	Purge           bool     `json:"purge"`
}

type installOwnershipManifest struct {
	SchemaVersion int      `json:"schema_version"`
	Product       string   `json:"product"`
	Paths         []string `json:"paths"`
}

type uninstallOptions struct {
	ConfigPath string
	InstallDir string
	Purge      bool
	Force      bool
	Yes        bool
	DryRun     bool
}

var uninstallBundleFiles = []string{
	"CHANGELOG.md", "LICENSE", "LICENSING.md", "NOTICE", "README.md",
	"SBOM.cdx.json", "SOURCE.md", "THIRD_PARTY_NOTICES.txt", "TRADEMARKS.md",
	"config.example.yml", "contextbridge-completion.ps1", "install.ps1", "install.sh",
}

var uninstallBundleDirectories = []string{"LICENSES", "deploy", "docs", "examples"}

const installOwnershipManifestName = ".contextbridge-install.json"

func uninstallCommand(args []string) error {
	flags := flag.NewFlagSet("uninstall", flag.ContinueOnError)
	options := uninstallOptions{}
	flags.StringVar(&options.ConfigPath, "config", defaultConfigPath(), "config path")
	flags.StringVar(&options.InstallDir, "install-dir", "", "installation directory; normally detected")
	flags.BoolVar(&options.Purge, "purge", false, "also remove locally managed config, credentials, jobs, models, and runtime data")
	flags.BoolVar(&options.Force, "force", false, "stop even while ContextBridge jobs are active or the config cannot be read")
	flags.BoolVar(&options.Yes, "yes", false, "confirm the displayed uninstall plan non-interactively")
	flags.BoolVar(&options.DryRun, "dry-run", false, "show the exact plan without stopping or removing anything")
	if err := flags.Parse(args); err != nil {
		return err
	}
	if flags.NArg() != 0 {
		return errors.New("usage: contextbridge uninstall [--config PATH] [--install-dir PATH] [--purge] [--force] [--yes] [--dry-run]")
	}
	plan, err := buildUninstallPlan(options)
	if err != nil {
		return err
	}
	printUninstallPlan(plan)
	if options.DryRun {
		fmt.Println("Dry run only · nothing was stopped or removed.")
		return nil
	}
	if !options.Yes {
		confirmed, err := confirmUninstall(plan.Purge)
		if err != nil {
			return err
		}
		if !confirmed {
			return errors.New("uninstall cancelled")
		}
	}
	if err := stopForUninstall(plan.ConfigPath, options.Force); err != nil {
		return err
	}
	return executeUninstall(plan)
}

func buildUninstallPlan(options uninstallOptions) (uninstallPlan, error) {
	current, err := os.Executable()
	if err != nil {
		return uninstallPlan{}, fmt.Errorf("locate running executable: %w", err)
	}
	current, err = filepath.Abs(current)
	if err != nil {
		return uninstallPlan{}, fmt.Errorf("resolve running executable: %w", err)
	}
	installDir, err := discoverInstallDirectory(options.InstallDir, current)
	if err != nil {
		return uninstallPlan{}, err
	}
	configPath, err := filepath.Abs(options.ConfigPath)
	if err != nil {
		return uninstallPlan{}, fmt.Errorf("resolve config path: %w", err)
	}
	binaryName := "contextbridge"
	if runtime.GOOS == "windows" {
		binaryName += ".exe"
	}
	plan := uninstallPlan{
		InstallDir:    installDir,
		InstallBinary: filepath.Join(installDir, binaryName),
		CurrentBinary: current,
		ConfigPath:    configPath,
		Purge:         options.Purge,
	}
	for _, name := range uninstallBundleFiles {
		plan.ProgramPaths = appendExistingOwnedPath(plan.ProgramPaths, filepath.Join(installDir, name), installDir, true)
	}
	for _, name := range uninstallBundleDirectories {
		plan.ProgramPaths = appendExistingOwnedPath(plan.ProgramPaths, filepath.Join(installDir, name), installDir, false)
	}
	plan.ProgramPaths = appendExistingOwnedPath(plan.ProgramPaths, plan.InstallBinary, installDir, true)
	plan.ProgramPaths = append(plan.ProgramPaths, managedCommandFiles(installDir)...)
	plan, err = addInstallOwnershipManifestPaths(plan)
	if err != nil {
		return uninstallPlan{}, err
	}
	plan.CommandPaths = discoverManagedCommandPaths(plan.InstallBinary, current)
	plan.CleanupDirs = []string{installDir}

	if options.Purge {
		plan, err = addPurgePaths(plan, options.Force)
		if err != nil {
			return uninstallPlan{}, err
		}
	} else if _, err := os.Stat(configPath); err == nil {
		plan.PreservedPaths = append(plan.PreservedPaths, configPath)
	}
	plan.ProgramPaths = uniqueCleanPaths(plan.ProgramPaths)
	plan.CommandPaths = uniqueCleanPaths(plan.CommandPaths)
	plan.PurgePaths = uniqueCleanPaths(plan.PurgePaths)
	plan.PreservedPaths = uniqueCleanPaths(plan.PreservedPaths)
	plan.CleanupDirs = uniqueCleanPaths(plan.CleanupDirs)
	if err := addPlatformUninstallPlan(&plan); err != nil {
		return uninstallPlan{}, err
	}
	return plan, nil
}

func addInstallOwnershipManifestPaths(plan uninstallPlan) (uninstallPlan, error) {
	manifestPath := filepath.Join(plan.InstallDir, installOwnershipManifestName)
	raw, err := readSmallRegularFile(manifestPath, 1<<20)
	if os.IsNotExist(err) {
		return plan, nil
	}
	if err != nil {
		return uninstallPlan{}, fmt.Errorf("read installer ownership manifest: %w", err)
	}
	decoder := json.NewDecoder(strings.NewReader(string(raw)))
	decoder.DisallowUnknownFields()
	manifest := installOwnershipManifest{}
	if err := decoder.Decode(&manifest); err != nil {
		return uninstallPlan{}, fmt.Errorf("parse installer ownership manifest: %w", err)
	}
	if err := decoder.Decode(&struct{}{}); !errors.Is(err, io.EOF) {
		return uninstallPlan{}, errors.New("installer ownership manifest contains trailing data")
	}
	if manifest.SchemaVersion != 1 || manifest.Product != "ContextBridge" {
		return uninstallPlan{}, errors.New("installer ownership manifest has an unsupported identity or schema")
	}
	if len(manifest.Paths) == 0 || len(manifest.Paths) > 1024 {
		return uninstallPlan{}, errors.New("installer ownership manifest has an invalid path count")
	}
	seen := map[string]bool{}
	hasBinary := false
	hasMarker := false
	for _, relative := range manifest.Paths {
		target, normalized, err := ownedManifestTarget(plan.InstallDir, relative)
		if err != nil {
			return uninstallPlan{}, err
		}
		key := pathComparisonKey(target)
		if seen[key] {
			return uninstallPlan{}, fmt.Errorf("installer ownership manifest repeats %q", normalized)
		}
		seen[key] = true
		if samePath(target, plan.InstallBinary) {
			hasBinary = true
		}
		if samePath(target, filepath.Join(plan.InstallDir, "config.example.yml")) {
			hasMarker = true
		}
		info, err := os.Lstat(target)
		if os.IsNotExist(err) {
			continue
		}
		if err != nil {
			return uninstallPlan{}, fmt.Errorf("inspect installer-owned path %q: %w", normalized, err)
		}
		if info.Mode()&os.ModeSymlink != 0 || (!info.Mode().IsRegular() && !info.IsDir()) {
			return uninstallPlan{}, fmt.Errorf("installer-owned path %q is not a regular file or directory", normalized)
		}
		plan.ProgramPaths = append(plan.ProgramPaths, target)
	}
	if !hasBinary || !hasMarker {
		return uninstallPlan{}, errors.New("installer ownership manifest does not bind the ContextBridge binary and installer marker")
	}
	plan.ProgramPaths = append(plan.ProgramPaths, manifestPath)
	return plan, nil
}

func ownedManifestTarget(installDir, relative string) (string, string, error) {
	if relative == "" || strings.Contains(relative, "\\") || pathpkg.IsAbs(relative) || pathpkg.Clean(relative) != relative || relative == "." || relative == ".." || strings.HasPrefix(relative, "../") {
		return "", "", fmt.Errorf("installer ownership manifest contains unsafe relative path %q", relative)
	}
	topLevel := strings.Split(relative, "/")[0]
	switch strings.ToLower(topLevel) {
	case "config.yml", "data", "inbox", "models":
		return "", "", fmt.Errorf("installer ownership manifest attempts to own mutable data path %q", relative)
	}
	target := filepath.Join(installDir, filepath.FromSlash(relative))
	if !pathInside(target, installDir) {
		return "", "", fmt.Errorf("installer ownership manifest path %q escapes the installation", relative)
	}
	return target, relative, nil
}

func discoverInstallDirectory(explicit, current string) (string, error) {
	candidates := []string{}
	if strings.TrimSpace(explicit) != "" {
		candidates = append(candidates, explicit)
	} else {
		candidates = append(candidates, filepath.Dir(current))
		if value := strings.TrimSpace(os.Getenv("CONTEXTBRIDGE_HOME")); value != "" {
			candidates = append(candidates, value)
		}
		if home, err := os.UserHomeDir(); err == nil {
			if runtime.GOOS == "windows" {
				if local := strings.TrimSpace(os.Getenv("LOCALAPPDATA")); local != "" {
					candidates = append(candidates, filepath.Join(local, "ContextBridge"))
				}
			} else {
				candidates = append(candidates, filepath.Join(home, ".local", "share", "contextbridge"))
			}
		}
		candidates = append(candidates, symlinkInstallCandidates(filepath.Dir(current))...)
	}
	seen := map[string]bool{}
	for _, candidate := range candidates {
		absolute, err := filepath.Abs(candidate)
		if err != nil {
			continue
		}
		key := pathComparisonKey(absolute)
		if seen[key] {
			continue
		}
		seen[key] = true
		if validInstallDirectory(absolute) {
			return filepath.Clean(absolute), nil
		}
	}
	if strings.TrimSpace(explicit) != "" {
		return "", fmt.Errorf("%s is not a recognized ContextBridge installation directory", explicit)
	}
	return "", errors.New("could not identify the ContextBridge installation; use --install-dir with the directory created by the installer")
}

func validInstallDirectory(path string) bool {
	absolute, err := filepath.Abs(path)
	if err != nil || dangerousRoot(absolute) {
		return false
	}
	binary := filepath.Join(absolute, "contextbridge")
	if runtime.GOOS == "windows" {
		binary += ".exe"
	}
	info, err := os.Lstat(binary)
	if err != nil || !info.Mode().IsRegular() || info.Mode()&os.ModeSymlink != 0 {
		return false
	}
	marker, err := os.Lstat(filepath.Join(absolute, "config.example.yml"))
	return err == nil && marker.Mode().IsRegular() && marker.Mode()&os.ModeSymlink == 0
}

func dangerousRoot(path string) bool {
	volume := filepath.VolumeName(path)
	root := string(filepath.Separator)
	if volume != "" {
		root = volume + string(filepath.Separator)
	}
	if samePath(path, root) {
		return true
	}
	if home, err := os.UserHomeDir(); err == nil && samePath(path, home) {
		return true
	}
	return false
}

func symlinkInstallCandidates(directory string) []string {
	entries, err := os.ReadDir(directory)
	if err != nil {
		return nil
	}
	var candidates []string
	for _, entry := range entries {
		if entry.Type()&os.ModeSymlink == 0 {
			continue
		}
		resolved, err := filepath.EvalSymlinks(filepath.Join(directory, entry.Name()))
		if err == nil && strings.EqualFold(filepath.Base(resolved), "contextbridge") {
			candidates = append(candidates, filepath.Dir(resolved))
		}
	}
	return candidates
}

func appendExistingOwnedPath(paths []string, target, root string, regularOnly bool) []string {
	info, err := os.Lstat(target)
	if err != nil || info.Mode()&os.ModeSymlink != 0 || !pathInside(target, root) {
		return paths
	}
	if regularOnly && !info.Mode().IsRegular() {
		return paths
	}
	if !regularOnly && !info.IsDir() {
		return paths
	}
	return append(paths, target)
}

func managedCommandFiles(installDir string) []string {
	entries, err := os.ReadDir(installDir)
	if err != nil {
		return nil
	}
	var paths []string
	for _, entry := range entries {
		if entry.IsDir() || !strings.EqualFold(filepath.Ext(entry.Name()), ".cmd") {
			continue
		}
		path := filepath.Join(installDir, entry.Name())
		raw, err := readSmallRegularFile(path, 4096)
		if err == nil && (bytesStartWith(raw, ":: ContextBridge managed cb alias") || bytesStartWith(raw, ":: ContextBridge managed custom command")) {
			paths = append(paths, path)
		}
	}
	return paths
}

func discoverManagedCommandPaths(installBinary, current string) []string {
	var directories []string
	if env := strings.TrimSpace(os.Getenv("CONTEXTBRIDGE_BIN_DIR")); env != "" {
		directories = append(directories, env)
	}
	if home, err := os.UserHomeDir(); err == nil && runtime.GOOS != "windows" {
		directories = append(directories, filepath.Join(home, ".local", "bin"))
	}
	directories = append(directories, filepath.Dir(current))
	var paths []string
	for _, directory := range uniqueCleanPaths(directories) {
		entries, err := os.ReadDir(directory)
		if err != nil {
			continue
		}
		for _, entry := range entries {
			path := filepath.Join(directory, entry.Name())
			if entry.Type()&os.ModeSymlink != 0 {
				resolved, err := filepath.EvalSymlinks(path)
				if err == nil && samePath(resolved, installBinary) {
					paths = append(paths, path)
				}
				continue
			}
			if samePath(path, current) && (strings.EqualFold(entry.Name(), "contextbridge") || strings.EqualFold(entry.Name(), "contextbridge.exe")) {
				paths = append(paths, path)
				continue
			}
			raw, err := readSmallRegularFile(path, 4096)
			if err == nil && (bytesStartWith(raw, "#!/bin/sh\n# ContextBridge managed cb alias") || bytesStartWith(raw, "#!/bin/sh\n# ContextBridge managed command alias")) {
				paths = append(paths, path)
			}
		}
	}
	return paths
}

func addPurgePaths(plan uninstallPlan, force bool) (uninstallPlan, error) {
	configDir := filepath.Dir(plan.ConfigPath)
	if info, err := os.Lstat(plan.ConfigPath); err == nil && (info.Mode().IsRegular() || info.Mode()&os.ModeSymlink != 0) {
		plan.PurgePaths = append(plan.PurgePaths, plan.ConfigPath)
	}
	cfg, err := config.Load(plan.ConfigPath)
	if err != nil {
		if !os.IsNotExist(err) && !force {
			return uninstallPlan{}, fmt.Errorf("cannot determine managed purge paths from %s: %w; repair the config or use --force to remove only discoverable local paths", plan.ConfigPath, err)
		}
		candidates := []string{
			filepath.Join(configDir, "data"),
			filepath.Join(configDir, "inbox"),
			filepath.Join(configDir, "models"),
			filepath.Join(configDir, "data", "rag"),
		}
		for _, candidate := range candidates {
			if safeManagedDataPath(candidate, configDir, plan.InstallDir) {
				plan.PurgePaths = append(plan.PurgePaths, candidate)
			}
		}
		if !os.IsNotExist(err) {
			plan.PreservedPaths = append(plan.PreservedPaths, "unreadable config: external managed paths could not be discovered")
		}
		plan.CleanupDirs = append(plan.CleanupDirs, configDir)
		return plan, nil
	}
	candidates := []string{cfg.Storage.Directory, cfg.Storage.Inbox, cfg.Storage.Models, cfg.RAG.Directory}
	for _, candidate := range candidates {
		if strings.TrimSpace(candidate) == "" {
			continue
		}
		if safeManagedDataPath(candidate, configDir, plan.InstallDir) {
			plan.PurgePaths = append(plan.PurgePaths, candidate)
		} else {
			plan.PreservedPaths = append(plan.PreservedPaths, candidate+" (outside managed roots or linked)")
		}
	}
	plan.CleanupDirs = append(plan.CleanupDirs, configDir)
	return plan, nil
}

func safeManagedDataPath(target string, roots ...string) bool {
	absolute, err := filepath.Abs(target)
	if err != nil || dangerousRoot(absolute) {
		return false
	}
	for _, root := range roots {
		root, err = filepath.Abs(root)
		if err != nil || dangerousRoot(root) || samePath(absolute, root) || !pathInside(absolute, root) {
			continue
		}
		resolved, err := resolveExistingPrefix(absolute)
		if err != nil {
			return false
		}
		resolvedRoot, err := filepath.EvalSymlinks(root)
		if err != nil {
			resolvedRoot = root
		}
		if pathInside(resolved, resolvedRoot) {
			return true
		}
	}
	return false
}

func resolveExistingPrefix(path string) (string, error) {
	candidate := path
	for {
		if _, err := os.Lstat(candidate); err == nil {
			resolved, err := filepath.EvalSymlinks(candidate)
			if err != nil {
				return "", err
			}
			relative, err := filepath.Rel(candidate, path)
			if err != nil || strings.HasPrefix(relative, "..") {
				return "", errors.New("cannot resolve managed data path")
			}
			return filepath.Join(resolved, relative), nil
		} else if !os.IsNotExist(err) {
			return "", err
		}
		parent := filepath.Dir(candidate)
		if parent == candidate {
			return "", errors.New("no existing path prefix")
		}
		candidate = parent
	}
}

func readSmallRegularFile(path string, limit int64) ([]byte, error) {
	info, err := os.Lstat(path)
	if err != nil {
		return nil, err
	}
	if !info.Mode().IsRegular() || info.Mode()&os.ModeSymlink != 0 || info.Size() > limit {
		return nil, errors.New("not a bounded regular file")
	}
	return os.ReadFile(path)
}

func bytesStartWith(raw []byte, prefix string) bool {
	return strings.HasPrefix(string(raw), prefix)
}

func uniqueCleanPaths(paths []string) []string {
	seen := map[string]bool{}
	result := make([]string, 0, len(paths))
	for _, path := range paths {
		if strings.TrimSpace(path) == "" {
			continue
		}
		clean := filepath.Clean(path)
		key := pathComparisonKey(clean)
		if !seen[key] {
			seen[key] = true
			result = append(result, clean)
		}
	}
	sort.Slice(result, func(i, j int) bool {
		if pathDepth(result[i]) == pathDepth(result[j]) {
			return result[i] < result[j]
		}
		return pathDepth(result[i]) > pathDepth(result[j])
	})
	return result
}

func pathDepth(path string) int {
	return strings.Count(filepath.Clean(path), string(filepath.Separator))
}

func pathComparisonKey(path string) string {
	path = filepath.Clean(path)
	if runtime.GOOS == "windows" {
		return strings.ToLower(path)
	}
	return path
}

func samePath(left, right string) bool {
	leftAbs, leftErr := filepath.Abs(left)
	rightAbs, rightErr := filepath.Abs(right)
	return leftErr == nil && rightErr == nil && pathComparisonKey(leftAbs) == pathComparisonKey(rightAbs)
}

func pathInside(path, root string) bool {
	pathAbs, pathErr := filepath.Abs(path)
	rootAbs, rootErr := filepath.Abs(root)
	if pathErr != nil || rootErr != nil || samePath(pathAbs, rootAbs) {
		return false
	}
	relative, err := filepath.Rel(rootAbs, pathAbs)
	return err == nil && relative != "." && relative != ".." && !strings.HasPrefix(relative, ".."+string(filepath.Separator)) && !filepath.IsAbs(relative)
}

func printUninstallPlan(plan uninstallPlan) {
	mode := "program and integrations only"
	if plan.Purge {
		mode = "PURGE program plus locally managed data"
	}
	fmt.Printf("ContextBridge uninstall plan  [%s]\n", mode)
	fmt.Printf("  Installation: %s\n", plan.InstallDir)
	if plan.Purge {
		fmt.Printf("  Config:       remove %s\n", plan.ConfigPath)
	} else {
		fmt.Printf("  Config:       preserve %s\n", plan.ConfigPath)
	}
	fmt.Printf("  Program:      %d owned path(s)\n", len(plan.ProgramPaths)+len(plan.CommandPaths))
	for _, path := range plan.ProgramPaths {
		fmt.Printf("  Remove file:  %s\n", path)
	}
	for _, path := range plan.CommandPaths {
		fmt.Printf("  Remove command: %s\n", path)
	}
	if plan.Purge {
		fmt.Printf("  Managed data: %d path(s)\n", len(plan.PurgePaths))
		for _, path := range plan.PurgePaths {
			fmt.Printf("  Remove data:  %s\n", path)
		}
	}
	for _, path := range plan.PreservedPaths {
		fmt.Printf("  Preserved:    %s\n", path)
	}
	fmt.Println("  Integrations: remove autostart, shortcuts, PATH entry, and shell completion only when ownership evidence matches")
	for _, path := range plan.CleanupDirs {
		fmt.Printf("  Remove if empty: %s\n", path)
	}
}

func confirmUninstall(purge bool) (bool, error) {
	info, err := os.Stdin.Stat()
	if err != nil || info.Mode()&os.ModeCharDevice == 0 {
		return false, errors.New("uninstall needs interactive confirmation; inspect --dry-run, then pass --yes for unattended use")
	}
	reader := bufio.NewReader(os.Stdin)
	if purge {
		fmt.Print("Type PURGE to remove the program and locally managed data: ")
		line, err := reader.ReadString('\n')
		return err == nil && strings.TrimSpace(line) == "PURGE", err
	}
	fmt.Print("Remove ContextBridge program files and integrations? [y/N] ")
	line, err := reader.ReadString('\n')
	answer := strings.ToLower(strings.TrimSpace(line))
	return err == nil && (answer == "y" || answer == "yes"), err
}

func stopForUninstall(configPath string, force bool) error {
	cfg, err := config.Load(configPath)
	if err != nil {
		if os.IsNotExist(err) {
			return nil
		}
		if force {
			fmt.Fprintf(os.Stderr, "ContextBridge: config could not be read; continuing because --force was supplied: %v\n", err)
			return nil
		}
		return fmt.Errorf("cannot verify a safe shutdown from the config: %w; use --force only if interrupting active work is acceptable", err)
	}
	endpoint, err := localControlURL(cfg.Server.Listen)
	if err != nil {
		if force {
			return nil
		}
		return err
	}
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	err = requestContextBridgeStop(ctx, &http.Client{Timeout: 20 * time.Second}, endpoint, cfg.Server.Token, force)
	if err == nil {
		return nil
	}
	var networkError net.Error
	if errors.As(err, &networkError) {
		if !networkError.Timeout() || force {
			return nil
		}
		return fmt.Errorf("ContextBridge shutdown timed out: %w; retry or use --force only if interrupting active work is acceptable", err)
	}
	return fmt.Errorf("ContextBridge did not stop cleanly: %w", err)
}
